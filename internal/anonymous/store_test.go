package anonymous

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("fixture RNG failure") }

type beforeVerificationInspectionHook struct {
	once sync.Once
	run  func() error
}

func (h *beforeVerificationInspectionHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *beforeVerificationInspectionHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		var err error
		if command.Name() == "evalsha" && len(command.Args()) > 1 && command.Args()[1] == inspectVerificationScript.Hash() {
			h.once.Do(func() { err = h.run() })
		}
		if err != nil {
			return err
		}
		return next(ctx, command)
	}
}

func (h *beforeVerificationInspectionHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

type redisFixture struct {
	store  *Store
	client *redis.Client
	prefix string
	clock  *testClock
}

func guardedRedisFixtureOptions(raw string) (*redis.Options, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Scheme != "redis" && parsed.Scheme != "rediss") || parsed.Path != "/15" {
		return nil, errors.New("unsafe Redis fixture URL")
	}
	options, err := redis.ParseURL(raw)
	if err != nil || options.Network != "tcp" || options.DB != 15 {
		return nil, errors.New("unsafe Redis fixture URL")
	}
	host, _, err := net.SplitHostPort(options.Addr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return nil, errors.New("unsafe Redis fixture URL")
	}
	options.MaxRetries = -1
	options.DialTimeout = time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	return options, nil
}

func newRedisFixture(t *testing.T, mutate func(*Config)) *redisFixture {
	t.Helper()
	url := os.Getenv("NODELANE_TEST_REDIS_URL")
	if url == "" {
		t.Skip("NODELANE_TEST_REDIS_URL is required for real Redis tests")
	}
	options, err := guardedRedisFixtureOptions(url)
	if err != nil {
		t.Fatalf("unsafe Redis fixture configuration: %v", err)
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("test Redis unavailable: %v", err)
	}
	marker, err := client.Get(ctx, "nodelane:test:marker").Result()
	if err != nil || marker != "bff_fixture_v1" {
		_ = client.Close()
		t.Fatalf("unsafe Redis fixture marker: value=%q err=%v", marker, err)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	prefix := "ctl_test:anonymous:" + base64.RawURLEncoding.EncodeToString(random)
	clock := &testClock{now: time.Date(2026, 9, 6, 1, 2, 3, 456000000, time.UTC)}
	config := Config{
		Client:           client,
		Prefix:           prefix,
		CredentialPepper: []byte("credential-pepper-is-independent-32"),
		ReplayKey:        []byte("replay-key-is-exactly-32-bytes!!"),
		FenceOwnerToken:  []byte("fence-owner-token-is-independent-32"),
		Clock:            clock.Now,
		Random:           rand.Reader,
		PublicDomain:     "tunnel.test",
		TCPPorts:         []uint16{21001, 21002, 21003},
		UDPPorts:         []uint16{22001, 22002, 22003},
	}
	if mutate != nil {
		mutate(&config)
	}
	store, err := NewStore(config)
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	f := &redisFixture{store: store, client: client, prefix: prefix, clock: clock}
	t.Cleanup(func() {
		cleanupFixtureKeys(t, client, prefix)
		_ = client.Close()
	})
	return f
}

func cleanupFixtureKeys(t *testing.T, client *redis.Client, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, prefix+":*", 100).Result()
		if err != nil {
			t.Errorf("scan fixture keys: %v", err)
			return
		}
		if len(keys) != 0 {
			if err := client.Unlink(ctx, keys...).Err(); err != nil {
				t.Errorf("unlink fixture keys: %v", err)
				return
			}
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}

func (f *redisFixture) ready(t *testing.T) {
	t.Helper()
	observed, err := f.store.ObserveResourceFence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkResourcesVerified(context.Background(), observed); err != nil {
		t.Fatal(err)
	}
}

func TestGuardedRedisFixtureOptions(t *testing.T) {
	for _, raw := range []string{
		"redis://127.0.0.1:6379/0",
		"redis://localhost:6379/15",
		"redis://127.0.0.1:6379/15?protocol=3",
		"redis://127.0.0.1:6379/15#fragment",
		"http://127.0.0.1:6379/15",
		"redis://192.0.2.1:6379/15",
	} {
		if _, err := guardedRedisFixtureOptions(raw); err == nil {
			t.Fatalf("unsafe fixture URL accepted: %q", raw)
		}
	}
	options, err := guardedRedisFixtureOptions("redis://:synthetic@127.0.0.1:6379/15")
	if err != nil || options.DB != 15 || options.Addr != "127.0.0.1:6379" {
		t.Fatalf("safe fixture URL rejected: %#v %v", options, err)
	}
}

func allocationRequest(key string) AllocateRequest {
	return AllocateRequest{
		InstallationID: "installation-a",
		NetworkKey:     "192.0.2.15",
		IdempotencyKey: key,
		Protocol:       ProtocolHTTP,
		LocalHost:      "127.0.0.1",
		LocalPort:      3000,
	}
}

func TestAllocateRequiresExplicitResourceVerificationAfterRedisLoss(t *testing.T) {
	f := newRedisFixture(t, nil)
	if _, err := f.store.Allocate(context.Background(), allocationRequest("ready-gate")); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("allocation without verified resource state error=%v", err)
	}
	f.ready(t)
	got, err := f.store.Allocate(context.Background(), allocationRequest("ready-gate"))
	if err != nil || got.CredentialToken == "" {
		t.Fatalf("verified allocation failed: %#v %v", got, err)
	}
	cleanupFixtureKeys(t, f.client, f.prefix)
	if _, err := f.store.Allocate(context.Background(), allocationRequest("after-loss")); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("allocation after Redis loss did not fail closed: %v", err)
	}
}

func TestResourceFenceUsesRedisRunIDOwnerAndRevisionCAS(t *testing.T) {
	f := newRedisFixture(t, nil)
	ctx := context.Background()
	observed, err := f.store.ObserveResourceFence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.RedisRunID) != 40 || observed.Revision != "" {
		t.Fatalf("unexpected initial fence: %#v", observed)
	}
	marked, err := f.store.MarkResourcesVerified(ctx, observed)
	if err != nil {
		t.Fatal(err)
	}
	if marked.RedisRunID != observed.RedisRunID || !validRandomName(marked.Revision, "afr_", 16) {
		t.Fatalf("invalid marked fence: %#v", marked)
	}
	if _, err := f.store.MarkResourcesVerified(ctx, observed); !errors.Is(err, ErrFenceConflict) {
		t.Fatalf("stale fence observation reopened allocations: %v", err)
	}

	other, err := NewStore(Config{
		Client: f.client, Prefix: f.prefix,
		CredentialPepper: []byte("credential-pepper-is-independent-32"),
		ReplayKey:        []byte("replay-key-is-exactly-32-bytes!!"),
		FenceOwnerToken:  []byte("different-fence-owner-token-32-bytes"),
		Clock:            f.clock.Now, Random: rand.Reader, PublicDomain: "tunnel.test",
		TCPPorts: []uint16{21001}, UDPPorts: []uint16{22001},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherObserved, err := other.ObserveResourceFence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.MarkResourcesVerified(ctx, otherObserved); !errors.Is(err, ErrFenceConflict) {
		t.Fatalf("different owner reopened allocations: %v", err)
	}

	if err := f.store.BlockAllocations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Allocate(ctx, allocationRequest("blocked-fence")); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("blocked fence allowed allocation: %v", err)
	}
	reconciled, err := f.store.ObserveResourceFence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkResourcesVerified(ctx, reconciled); err != nil {
		t.Fatalf("same owner could not reopen after a fresh observation: %v", err)
	}
}

func TestAssertFreshNamespaceAcceptsOnlyUninitializedBlockedMarker(t *testing.T) {
	f := newRedisFixture(t, nil)
	ctx := context.Background()
	if err := f.store.BlockAllocations(ctx); err != nil {
		t.Fatal(err)
	}
	observed, err := f.store.ObserveResourceFence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AssertFreshNamespace(ctx, observed); err != nil {
		t.Fatalf("fresh blocked namespace rejected: %v", err)
	}
	after, err := f.store.ObserveResourceFence(ctx)
	if err != nil || after != observed {
		t.Fatalf("fresh assertion mutated fence: after=%#v want=%#v err=%v", after, observed, err)
	}
}

func TestPrepareFreshInitializationRejectsReadyNamespaceWithoutMutation(t *testing.T) {
	f := newRedisFixture(t, nil)
	ctx := context.Background()
	prepared, err := f.store.PrepareFreshInitialization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AssertFreshNamespace(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	marked, err := f.store.MarkResourcesVerified(ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.client.HGetAll(ctx, f.store.readyKey()).Result()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.PrepareFreshInitialization(ctx); !errors.Is(err, ErrResourcesUnverified) || got != (ResourceFence{}) {
		t.Fatalf("ready namespace prepared again: fence=%#v err=%v", got, err)
	}
	after, err := f.client.HGetAll(ctx, f.store.readyKey()).Result()
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected prepare mutated ready marker: after=%v before=%v err=%v", after, before, err)
	}
	observed, err := f.store.ObserveResourceFence(ctx)
	if err != nil || observed != marked {
		t.Fatalf("rejected prepare changed fence: got=%#v want=%#v err=%v", observed, marked, err)
	}
}

func TestConcurrentFreshInitializationLeavesOnlyNewestFenceEligible(t *testing.T) {
	f := newRedisFixture(t, nil)
	ctx := context.Background()
	type result struct {
		fence ResourceFence
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			fence, err := f.store.PrepareFreshInitialization(ctx)
			results <- result{fence: fence, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent prepare errors: first=%v second=%v", first.err, second.err)
	}
	older, newest := first.fence, second.fence
	if older.Generation > newest.Generation {
		older, newest = newest, older
	}
	if older.Generation == newest.Generation {
		t.Fatalf("concurrent prepares reused generation: %#v %#v", older, newest)
	}
	if err := f.store.AssertFreshNamespace(ctx, older); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("older fresh fence stayed eligible: %v", err)
	}
	if err := f.store.AssertFreshNamespace(ctx, newest); err != nil {
		t.Fatalf("newest fresh fence rejected: %v", err)
	}
	marked, err := f.store.MarkResourcesVerified(ctx, newest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkResourcesVerified(ctx, older); !errors.Is(err, ErrFenceConflict) {
		t.Fatalf("older fresh fence won CAS: %v", err)
	}
	observed, err := f.store.ObserveResourceFence(ctx)
	if err != nil || observed != marked {
		t.Fatalf("loser changed winner marker: got=%#v want=%#v err=%v", observed, marked, err)
	}
}

func TestAssertFreshNamespaceRejectsInitializedOrPopulatedNamespace(t *testing.T) {
	t.Run("previous revision", func(t *testing.T) {
		f := newRedisFixture(t, nil)
		f.ready(t)
		ctx := context.Background()
		if err := f.store.BlockAllocations(ctx); err != nil {
			t.Fatal(err)
		}
		observed, err := f.store.ObserveResourceFence(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.AssertFreshNamespace(ctx, observed); !errors.Is(err, ErrResourcesUnverified) {
			t.Fatalf("initialized namespace accepted: %v", err)
		}
	})

	for _, suffix := range []string{
		":run:orphan",
		":active:installation:orphan",
		":resource:http:orphan",
		":rate:network:orphan",
		":replay:orphan",
		":verification:quarantine",
		":unknown",
	} {
		t.Run(suffix, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			ctx := context.Background()
			if err := f.store.BlockAllocations(ctx); err != nil {
				t.Fatal(err)
			}
			observed, err := f.store.ObserveResourceFence(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.client.Set(ctx, f.prefix+suffix, "orphan", 0).Err(); err != nil {
				t.Fatal(err)
			}
			if err := f.store.AssertFreshNamespace(ctx, observed); !errors.Is(err, ErrResourcesUnverified) {
				t.Fatalf("populated namespace %q accepted: %v", suffix, err)
			}
		})
	}
}

func TestAssertFreshNamespaceRejectsStaleFenceObservation(t *testing.T) {
	f := newRedisFixture(t, nil)
	ctx := context.Background()
	if err := f.store.BlockAllocations(ctx); err != nil {
		t.Fatal(err)
	}
	observed, err := f.store.ObserveResourceFence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.BlockAllocations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AssertFreshNamespace(ctx, observed); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("stale fresh observation accepted: %v", err)
	}
}

func TestResourceFenceRejectsMarkerFromAnotherRedisRun(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	if err := f.client.HSet(ctx, f.store.readyKey(), "node_run_id", strings.Repeat("0", 40)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Allocate(ctx, allocationRequest("stale-node-fence")); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("stale Redis run marker did not fail closed: %v", err)
	}
}

func TestResourceFenceRejectsObservationFromBeforeEveryBlock(t *testing.T) {
	for _, block := range []string{"explicit", "repeated", "quarantine"} {
		t.Run(block, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			allocation, err := f.store.Allocate(ctx, allocationRequest("fence-invalidated"))
			if err != nil {
				t.Fatal(err)
			}
			if block == "repeated" {
				if err := f.store.BlockAllocations(ctx); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := f.store.ObserveResourceFence(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if block == "quarantine" {
				if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
					t.Fatal(err)
				}
				if err := f.client.HDel(ctx, f.store.runKey(allocation.RunID), "proxy_name").Err(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.PendingVerification(ctx, 1); !errors.Is(err, ErrVerificationCorrupt) {
					t.Fatalf("corrupt item did not trigger quarantine: %v", err)
				}
			} else if err := f.store.BlockAllocations(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.MarkResourcesVerified(ctx, observed); !errors.Is(err, ErrFenceConflict) {
				t.Fatalf("observation predating %s reopened allocations: %v", block, err)
			}
			if _, err := f.store.Allocate(ctx, allocationRequest("stale-proof-reopen")); !errors.Is(err, ErrResourcesUnverified) {
				t.Fatalf("stale reconciliation proof left allocations open: %v", err)
			}
		})
	}
}

func TestBlockedFenceRejectsRunExpansionButAllowsDrain(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	allocation, err := f.store.Allocate(ctx, allocationRequest("fence-drain"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkConnected(ctx, allocation.RunID, allocation.ProxyName); err != nil {
		t.Fatal(err)
	}
	if err := f.store.BlockAllocations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Authorize(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("authorize passed a blocked fence: %v", err)
	}
	if _, err := f.store.Heartbeat(ctx, allocation.RunID, allocation.CredentialToken); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("heartbeat passed a blocked fence: %v", err)
	}
	if _, err := f.store.MarkConnected(ctx, allocation.RunID, allocation.ProxyName); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("mark connected passed a blocked fence: %v", err)
	}
	if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
		t.Fatalf("blocked fence prevented stop: %v", err)
	}
	items, err := f.store.PendingVerification(ctx, 1)
	if err != nil || len(items) != 1 || items[0].RunID != allocation.RunID {
		t.Fatalf("blocked fence prevented verification: %#v %v", items, err)
	}
	if err := f.store.ConfirmReleased(ctx, confirmedRelease(allocation.RunID, allocation.ProxyName)); err != nil {
		t.Fatalf("blocked fence prevented trusted release: %v", err)
	}
}

func TestAllocateCreatesIndependentRandomNamespacesAndStoresNoPlaintextCredential(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	got, err := f.store.Allocate(context.Background(), allocationRequest("namespaces"))
	if err != nil {
		t.Fatal(err)
	}
	if !validRandomName(got.RunID, "anr_", 16) || !validRandomName(got.ProxyName, "anon_", 16) {
		t.Fatalf("invalid run/proxy namespace: %#v", got)
	}
	label := strings.TrimSuffix(strings.TrimPrefix(got.PublicEndpoint, "anon-"), ".tunnel.test")
	if !strings.HasPrefix(got.PublicEndpoint, "anon-") || !validEncodedRandom(label, 16) {
		t.Fatalf("HTTP endpoint lacks 128 random bits: %q", got.PublicEndpoint)
	}
	credentialID, secret, ok := parseTestCredential(got.CredentialToken)
	if !ok || !validRandomName(credentialID, "nac_", 16) || len(secret) != 32 {
		t.Fatalf("invalid anonymous credential: id=%q secretBytes=%d", credentialID, len(secret))
	}
	if strings.TrimPrefix(got.RunID, "anr_") == strings.TrimPrefix(got.ProxyName, "anon_") ||
		strings.TrimPrefix(got.RunID, "anr_") == strings.TrimPrefix(credentialID, "nac_") ||
		strings.TrimPrefix(got.ProxyName, "anon_") == strings.TrimPrefix(credentialID, "nac_") {
		t.Fatal("independent identifiers reused random material")
	}
	keys, err := f.client.Keys(context.Background(), f.prefix+":*").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		typeOfKey, err := f.client.Type(context.Background(), key).Result()
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		switch typeOfKey {
		case "string":
			value, err := f.client.Get(context.Background(), key).Result()
			if err != nil {
				t.Fatal(err)
			}
			values = []string{value}
		case "hash":
			m, err := f.client.HGetAll(context.Background(), key).Result()
			if err != nil {
				t.Fatal(err)
			}
			for field, value := range m {
				values = append(values, field, value)
			}
		case "set":
			values, err = f.client.SMembers(context.Background(), key).Result()
			if err != nil {
				t.Fatal(err)
			}
		case "zset":
			values, err = f.client.ZRange(context.Background(), key, 0, -1).Result()
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, value := range values {
			if strings.Contains(value, got.CredentialToken) || strings.Contains(value, base64.RawURLEncoding.EncodeToString(secret)) {
				t.Fatalf("Redis value contains plaintext credential material in %s", key)
			}
		}
	}
}

func TestConcurrentSameIdempotencyKeyReturnsOneAllocationAndOneSuccessCount(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	const workers = 12
	start := make(chan struct{})
	results := make(chan Allocation, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-start
			got, err := f.store.Allocate(context.Background(), allocationRequest("same-key"))
			results <- got
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	var first Allocation
	fresh, replayed := 0, 0
	for range workers {
		got, err := <-results, <-errs
		if err != nil {
			t.Fatal(err)
		}
		if first.RunID == "" {
			first = got
		}
		if got.RunID != first.RunID || got.CredentialToken != first.CredentialToken || got.PublicEndpoint != first.PublicEndpoint || !got.ConnectDeadlineAt.Equal(first.ConnectDeadlineAt) {
			t.Fatalf("same idempotency key created or exposed a different allocation: %#v vs %#v", got, first)
		}
		if got.Replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != workers-1 {
		t.Fatalf("fresh=%d replayed=%d", fresh, replayed)
	}
	counts, err := readCounts(f, "installation-a", "192.0.2.15")
	if err != nil || counts.InstallationActive != 1 || counts.NetworkActive != 1 || counts.InstallationAllocations != 1 || counts.NetworkAllocations != 1 {
		t.Fatalf("idempotent allocation double-counted: %#v %v", counts, err)
	}
}

func TestReplayRecoveryDoesNotRequireNewRandomness(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	request := allocationRequest("rng-independent-replay")
	first, err := f.store.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(Config{
		Client: f.client, Prefix: f.prefix,
		CredentialPepper: []byte("credential-pepper-is-independent-32"),
		ReplayKey:        []byte("replay-key-is-exactly-32-bytes!!"),
		FenceOwnerToken:  []byte("fence-owner-token-is-independent-32"),
		Clock:            f.clock.Now, Random: failingReader{}, PublicDomain: "tunnel.test",
		TCPPorts: []uint16{21001}, UDPPorts: []uint16{22001},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.Allocate(context.Background(), request)
	if err != nil || !replayed.Replayed || replayed.RunID != first.RunID || replayed.CredentialToken != first.CredentialToken {
		t.Fatalf("committed replay depended on fresh RNG: %#v %v", replayed, err)
	}
}

func TestAllocateBindsIdempotencyToRequestDigest(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	first, err := f.store.Allocate(context.Background(), allocationRequest("digest"))
	if err != nil {
		t.Fatal(err)
	}
	changed := allocationRequest("digest")
	changed.LocalPort = 3001
	got, err := f.store.Allocate(context.Background(), changed)
	if !errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, Allocation{}) {
		t.Fatalf("changed request replayed secret: %#v %v", got, err)
	}
	changed = allocationRequest("digest")
	changed.NetworkKey = "198.51.100.22"
	got, err = f.store.Allocate(context.Background(), changed)
	if !errors.Is(err, ErrInstallationLimit) || errors.Is(err, ErrIdempotencyConflict) || !reflect.DeepEqual(got, Allocation{}) || first.RunID == "" {
		t.Fatalf("network is part of replay index, not request digest: %#v %v", got, err)
	}
}

func TestReplayRevalidatesCurrentRunOwnershipAndCredentialHash(t *testing.T) {
	for _, mutation := range []string{"run association", "credential hash"} {
		t.Run(mutation, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			request := allocationRequest("protected-replay")
			first, err := f.store.Allocate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "run association":
				otherRequest := allocationRequest("other-replay")
				otherRequest.InstallationID = "other-replay-installation"
				otherRequest.NetworkKey = "198.51.100.61"
				other, err := f.store.Allocate(context.Background(), otherRequest)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.client.HSet(context.Background(), f.store.replayKey(request.InstallationID, request.NetworkKey, request.IdempotencyKey), "run_key", f.store.runKey(other.RunID)).Err(); err != nil {
					t.Fatal(err)
				}
			case "credential hash":
				if err := f.client.HSet(context.Background(), f.store.runKey(first.RunID), "credential_hash", strings.Repeat("0", 64)).Err(); err != nil {
					t.Fatal(err)
				}
			}
			got, err := f.store.Allocate(context.Background(), request)
			if !errors.Is(err, ErrInvalidState) || !reflect.DeepEqual(got, Allocation{}) {
				t.Fatalf("corrupt current run replayed a credential: %#v %v", got, err)
			}
		})
	}
}

func TestReplayNeverReturnsCredentialAfterStopOrExactReplayExpiry(t *testing.T) {
	for _, state := range []string{"stopped", "replay expired"} {
		t.Run(state, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			request := allocationRequest("terminal-replay")
			first, err := f.store.Allocate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			var want error
			switch state {
			case "stopped":
				if _, err := f.store.RequestStop(context.Background(), first.RunID, first.CredentialToken); err != nil {
					t.Fatal(err)
				}
				want = ErrRunStopped
			case "replay expired":
				f.clock.Set(first.CreatedAt.Add(replayLifetime))
				want = ErrRunExpired
			}
			got, err := f.store.Allocate(context.Background(), request)
			if !errors.Is(err, want) || !reflect.DeepEqual(got, Allocation{}) {
				t.Fatalf("terminal replay exposed allocation: %#v %v", got, err)
			}
		})
	}
}

func TestReplayExpiryDoesNotStopLiveRun(t *testing.T) {
	for _, operation := range []string{"lookup", "allocation race"} {
		t.Run(operation, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			request := allocationRequest("expired-replay-live-run")
			allocation, err := f.store.Allocate(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			f.clock.Set(allocation.CreatedAt.Add(100 * time.Second))
			if _, err := f.store.MarkConnected(ctx, allocation.RunID, allocation.ProxyName); err != nil {
				t.Fatal(err)
			}
			before, err := f.client.HGetAll(ctx, f.store.runKey(allocation.RunID)).Result()
			if err != nil {
				t.Fatal(err)
			}
			f.clock.Set(allocation.CreatedAt.Add(2 * time.Minute))
			got, err := replayThroughOperation(f, request, operation)
			if !errors.Is(err, ErrRunExpired) || !reflect.DeepEqual(got, Allocation{}) {
				t.Fatalf("expired replay returned authorization: %#v %v", got, err)
			}
			after, err := f.client.HGetAll(ctx, f.store.runKey(allocation.RunID)).Result()
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("replay expiry mutated still-live run: before=%v after=%v err=%v", before, after, err)
			}
			if _, err := f.store.Authorize(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); err != nil {
				t.Fatalf("live lease was invalidated by replay expiry: %v", err)
			}
		})
	}
}

func TestExpiredReplayValidatesRunAssociationBeforeMutation(t *testing.T) {
	for _, operation := range []string{"lookup", "allocation race"} {
		t.Run(operation, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			request := allocationRequest("expired-replay-wrong-owner")
			first, err := f.store.Allocate(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			otherRequest := allocationRequest("expired-replay-other-owner")
			otherRequest.InstallationID = "another-replay-installation"
			other, err := f.store.Allocate(ctx, otherRequest)
			if err != nil {
				t.Fatal(err)
			}
			otherKey := f.store.runKey(other.RunID)
			if err := f.client.HSet(ctx, f.store.replayKey(request.InstallationID, request.NetworkKey, request.IdempotencyKey), "run_key", otherKey).Err(); err != nil {
				t.Fatal(err)
			}
			before, err := f.client.HGetAll(ctx, otherKey).Result()
			if err != nil {
				t.Fatal(err)
			}
			f.clock.Set(first.CreatedAt.Add(2 * time.Minute))
			got, err := replayThroughOperation(f, request, operation)
			if !errors.Is(err, ErrInvalidState) || !reflect.DeepEqual(got, Allocation{}) {
				t.Fatalf("invalid expired replay association accepted: %#v %v", got, err)
			}
			after, err := f.client.HGetAll(ctx, otherKey).Result()
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("expired corrupt replay mutated another run: before=%v after=%v err=%v", before, after, err)
			}
		})
	}
}

func replayThroughOperation(f *redisFixture, request AllocateRequest, operation string) (Allocation, error) {
	if operation == "lookup" {
		return f.store.Allocate(context.Background(), request)
	}
	requestHash, err := hashAllocationRequest(request)
	if err != nil {
		return Allocation{}, err
	}
	key := f.store.replayKey(request.InstallationID, request.NetworkKey, request.IdempotencyKey)
	args := make([]any, 22)
	for index := range args {
		args[index] = ""
	}
	args[0], args[10], args[21] = f.clock.Now().UnixMilli(), requestHash, f.store.prefix+":run:"
	values, err := allocateScript.Run(context.Background(), f.client, []string{
		f.store.readyKey(), key, "", "", "", "", "", "", f.store.verificationKey(), "",
	}, args...).Slice()
	if err != nil {
		return Allocation{}, err
	}
	allocation, _, err := f.store.decodeAllocateResult(key, requestHash, f.clock.Now(), values)
	return allocation, err
}

func TestAllocateEnforcesActiveLimitsUntilConfirmedRelease(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	first, err := f.store.Allocate(context.Background(), allocationRequest("first"))
	if err != nil {
		t.Fatal(err)
	}
	other := allocationRequest("second")
	other.NetworkKey = "198.51.100.44"
	if _, err := f.store.Allocate(context.Background(), other); !errors.Is(err, ErrInstallationLimit) {
		t.Fatalf("installation active limit error=%v", err)
	}
	for i := 0; i < 2; i++ {
		req := allocationRequest(fmt.Sprintf("network-%d", i))
		req.InstallationID = fmt.Sprintf("installation-network-%d", i)
		req.NetworkKey = "203.0.113.9"
		if _, err := f.store.Allocate(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	third := allocationRequest("network-third")
	third.InstallationID = "installation-network-third"
	third.NetworkKey = "203.0.113.9"
	if _, err := f.store.Allocate(context.Background(), third); !errors.Is(err, ErrNetworkLimit) {
		t.Fatalf("network active limit error=%v", err)
	}
	if _, err := f.store.RequestStop(context.Background(), first.RunID, first.CredentialToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Allocate(context.Background(), other); !errors.Is(err, ErrInstallationLimit) {
		t.Fatalf("stop released resource before data-plane confirmation: %v", err)
	}
	if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(first.RunID, first.ProxyName)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Allocate(context.Background(), other); err != nil {
		t.Fatalf("confirmed release did not free active slot: %v", err)
	}
}

func TestConcurrencyLimitsReportScopedEarliestVerificationRetry(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	first, err := f.store.Allocate(ctx, allocationRequest("active-retry-first"))
	if err != nil {
		t.Fatal(err)
	}
	other := allocationRequest("active-retry-other")
	other.NetworkKey = "198.51.100.71"
	_, err = f.store.Allocate(ctx, other)
	var installationLimit *ConcurrencyLimitError
	if !errors.As(err, &installationLimit) || !errors.Is(err, ErrInstallationLimit) || errors.Is(err, ErrNetworkLimit) ||
		installationLimit.Scope != LimitInstallation || installationLimit.RetryAfter != connectLifetime {
		t.Fatalf("installation retry metadata=%#v err=%v", installationLimit, err)
	}
	if _, err := f.store.MarkConnected(ctx, first.RunID, first.ProxyName); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Allocate(ctx, other)
	installationLimit = nil
	if !errors.As(err, &installationLimit) || installationLimit.RetryAfter != heartbeatLease {
		t.Fatalf("connected retry did not follow lease: %#v err=%v", installationLimit, err)
	}
	f.clock.Set(f.clock.Now().Add(10 * time.Second))
	if _, err := f.store.Heartbeat(ctx, first.RunID, first.CredentialToken); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Allocate(ctx, other)
	installationLimit = nil
	if !errors.As(err, &installationLimit) || installationLimit.RetryAfter != heartbeatLease {
		t.Fatalf("heartbeat retry did not follow renewed lease: %#v err=%v", installationLimit, err)
	}
	if _, err := f.store.RequestStop(ctx, first.RunID, first.CredentialToken); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Allocate(ctx, other)
	installationLimit = nil
	if !errors.As(err, &installationLimit) || installationLimit.RetryAfter != time.Millisecond {
		t.Fatalf("stopping retry did not report immediate verification eligibility: %#v err=%v", installationLimit, err)
	}

	for index := 0; index < 2; index++ {
		request := allocationRequest(fmt.Sprintf("network-retry-%d", index))
		request.InstallationID = fmt.Sprintf("network-retry-installation-%d", index)
		request.NetworkKey = "203.0.113.71"
		if _, err := f.store.Allocate(ctx, request); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			f.clock.Set(f.clock.Now().Add(time.Second))
		}
	}
	third := allocationRequest("network-retry-third")
	third.InstallationID = "network-retry-installation-third"
	third.NetworkKey = "203.0.113.71"
	_, err = f.store.Allocate(ctx, third)
	var networkLimit *ConcurrencyLimitError
	if !errors.As(err, &networkLimit) || !errors.Is(err, ErrNetworkLimit) || errors.Is(err, ErrInstallationLimit) ||
		networkLimit.Scope != LimitNetwork || networkLimit.RetryAfter != connectLifetime-time.Second {
		t.Fatalf("network retry metadata=%#v err=%v", networkLimit, err)
	}
}

func TestSuccessfulAllocationRateLimitsDoNotCountReplayOrRejectedAllocation(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	for i := 0; i < 5; i++ {
		req := allocationRequest(fmt.Sprintf("rate-%d", i))
		got, err := f.store.Allocate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if replay, err := f.store.Allocate(context.Background(), req); err != nil || !replay.Replayed {
			t.Fatalf("replay failed: %#v %v", replay, err)
		}
		if _, err := f.store.RequestStop(context.Background(), got.RunID, got.CredentialToken); err != nil {
			t.Fatal(err)
		}
		if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(got.RunID, got.ProxyName)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.store.Allocate(context.Background(), allocationRequest("rate-six"))
	var limited *RateLimitError
	if !errors.As(err, &limited) || !errors.Is(err, ErrRateLimited) || limited.Scope != LimitInstallation || limited.RetryAfter <= 0 || limited.RetryAfter > 10*time.Minute {
		t.Fatalf("installation rate error=%#v", err)
	}
	counts, err := readCounts(f, "installation-a", "192.0.2.15")
	if err != nil || counts.InstallationAllocations != 5 {
		t.Fatalf("replay/rejection changed success count: %#v %v", counts, err)
	}
}

func TestSuccessfulAllocationRateLimitsOneNormalizedNetworkKey(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	for i := 0; i < 20; i++ {
		req := allocationRequest(fmt.Sprintf("network-rate-%d", i))
		req.InstallationID = fmt.Sprintf("network-rate-installation-%d", i)
		got, err := f.store.Allocate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.RequestStop(context.Background(), got.RunID, got.CredentialToken); err != nil {
			t.Fatal(err)
		}
		if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(got.RunID, got.ProxyName)); err != nil {
			t.Fatal(err)
		}
	}
	request := allocationRequest("network-rate-21")
	request.InstallationID = "network-rate-installation-21"
	_, err := f.store.Allocate(context.Background(), request)
	var limited *RateLimitError
	if !errors.As(err, &limited) || limited.Scope != LimitNetwork || limited.RetryAfter <= 0 || limited.RetryAfter > 10*time.Minute {
		t.Fatalf("network rate error=%#v", err)
	}
}

func TestLifecycleUsesStrictAbsoluteDeadlinesAndKeepsExpiredResourceForVerification(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	allocation, err := f.store.Allocate(context.Background(), allocationRequest("lifecycle"))
	if err != nil {
		t.Fatal(err)
	}
	if allocation.ConnectDeadlineAt.Sub(f.clock.Now()) != 2*time.Minute || allocation.HardExpiresAt.Sub(f.clock.Now()) != time.Hour {
		t.Fatalf("wrong absolute deadlines: %#v", allocation)
	}
	if _, err := f.store.Authorize(context.Background(), allocation.RunID, allocation.CredentialToken, allocation.ProxyName); err != nil {
		t.Fatal(err)
	}
	f.clock.Set(allocation.ConnectDeadlineAt)
	if _, err := f.store.Authorize(context.Background(), allocation.RunID, allocation.CredentialToken, allocation.ProxyName); !errors.Is(err, ErrRunExpired) {
		t.Fatalf("exact pending deadline authorized: %v", err)
	}
	items, err := f.store.PendingVerification(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].RunID != allocation.RunID || items[0].ProxyName != allocation.ProxyName {
		t.Fatalf("expired run not retained for verification: %#v %v", items, err)
	}
	counts, err := readCounts(f, "installation-a", "192.0.2.15")
	if err != nil || counts.InstallationActive != 1 || counts.NetworkActive != 1 {
		t.Fatalf("authorization expiry released ownership: %#v %v", counts, err)
	}
	if _, err := f.store.Heartbeat(context.Background(), allocation.RunID, allocation.CredentialToken); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("expired pending run heartbeat error=%v", err)
	}
	if _, err := f.store.MarkConnected(context.Background(), allocation.RunID, allocation.ProxyName); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("expired pending run connected: %v", err)
	}
}

func TestConnectedHeartbeatLeaseCannotCrossHardLimit(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	allocation, err := f.store.Allocate(context.Background(), allocationRequest("heartbeat"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.store.MarkConnected(context.Background(), allocation.RunID, allocation.ProxyName)
	if err != nil || run.State != StateOnline || run.LeaseExpiresAt.Sub(f.clock.Now()) != 90*time.Second {
		t.Fatalf("mark connected result=%#v err=%v", run, err)
	}
	for elapsed := time.Minute; elapsed <= 59*time.Minute; elapsed += time.Minute {
		f.clock.Set(allocation.CreatedAt.Add(elapsed))
		if _, err := f.store.Heartbeat(context.Background(), allocation.RunID, allocation.CredentialToken); err != nil {
			t.Fatalf("heartbeat at %s: %v", elapsed, err)
		}
	}
	f.clock.Set(allocation.HardExpiresAt.Add(-30 * time.Second))
	heartbeat, err := f.store.Heartbeat(context.Background(), allocation.RunID, allocation.CredentialToken)
	if err != nil || !heartbeat.LeaseExpiresAt.Equal(allocation.HardExpiresAt) || heartbeat.DesiredState != DesiredRunning {
		t.Fatalf("heartbeat crossed hard cap: %#v %v", heartbeat, err)
	}
	f.clock.Set(allocation.HardExpiresAt)
	if _, err := f.store.Authorize(context.Background(), allocation.RunID, allocation.CredentialToken, allocation.ProxyName); !errors.Is(err, ErrRunExpired) {
		t.Fatalf("exact hard deadline authorized: %v", err)
	}
	if _, err := f.store.Heartbeat(context.Background(), allocation.RunID, allocation.CredentialToken); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("exact hard deadline renewed: %v", err)
	}
}

func TestRepeatedConnectionEvidenceUsesOnlineLeaseNotInitialDeadline(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	allocation, err := f.store.Allocate(context.Background(), allocationRequest("repeated-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.store.MarkConnected(context.Background(), allocation.RunID, allocation.ProxyName)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Set(allocation.CreatedAt.Add(time.Minute))
	heartbeat, err := f.store.Heartbeat(context.Background(), allocation.RunID, allocation.CredentialToken)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Set(allocation.ConnectDeadlineAt)
	repeated, err := f.store.MarkConnected(context.Background(), allocation.RunID, allocation.ProxyName)
	if err != nil || repeated.State != StateOnline || !repeated.LeaseExpiresAt.Equal(heartbeat.LeaseExpiresAt) || first.LeaseExpiresAt.Equal(heartbeat.LeaseExpiresAt) {
		t.Fatalf("online evidence incorrectly used initial deadline or renewed lease: first=%#v repeated=%#v heartbeat=%#v err=%v", first, repeated, heartbeat, err)
	}
}

func TestCredentialAndProxyAreBoundToRunAndAllMutationsFailClosed(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	a, err := f.store.Allocate(context.Background(), allocationRequest("proof-a"))
	if err != nil {
		t.Fatal(err)
	}
	bReq := allocationRequest("proof-b")
	bReq.InstallationID = "installation-b"
	b, err := f.store.Allocate(context.Background(), bReq)
	if err != nil {
		t.Fatal(err)
	}
	replacement := byte('A')
	if a.CredentialToken[len(a.CredentialToken)-1] == replacement {
		replacement = 'B'
	}
	wrong := a.CredentialToken[:len(a.CredentialToken)-1] + string(replacement)
	for name, operation := range map[string]func() error{
		"authorize wrong secret": func() error {
			_, err := f.store.Authorize(context.Background(), a.RunID, wrong, a.ProxyName)
			return err
		},
		"authorize other run": func() error {
			_, err := f.store.Authorize(context.Background(), b.RunID, a.CredentialToken, b.ProxyName)
			return err
		},
		"authorize proxy": func() error {
			_, err := f.store.Authorize(context.Background(), a.RunID, a.CredentialToken, b.ProxyName)
			return err
		},
		"heartbeat wrong": func() error { _, err := f.store.Heartbeat(context.Background(), a.RunID, wrong); return err },
		"stop wrong":      func() error { _, err := f.store.RequestStop(context.Background(), a.RunID, wrong); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, ErrInvalidCredential) {
				t.Fatalf("operation did not fail closed: %v", err)
			}
		})
	}
	if _, err := f.store.MarkConnected(context.Background(), a.RunID, b.ProxyName); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("trusted connection evidence accepted another run's proxy: %v", err)
	}
}

func TestAuthorizeLoginAuthenticatesRunWithoutProxyAndPreservesLifecycle(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	allocation, err := f.store.Allocate(context.Background(), allocationRequest("login-authorization"))
	if err != nil {
		t.Fatal(err)
	}

	run, err := f.store.AuthorizeLogin(context.Background(), allocation.RunID, allocation.CredentialToken)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != allocation.RunID || run.ProxyName != allocation.ProxyName || run.State != StateReserved || !run.LeaseExpiresAt.IsZero() {
		t.Fatalf("login authorization mutated or returned wrong run: %#v", run)
	}

	wrong := allocation.CredentialToken[:len(allocation.CredentialToken)-1] + "A"
	if wrong == allocation.CredentialToken {
		wrong = allocation.CredentialToken[:len(allocation.CredentialToken)-1] + "B"
	}
	if _, err := f.store.AuthorizeLogin(context.Background(), allocation.RunID, wrong); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("wrong login credential accepted: %v", err)
	}

	f.clock.Set(allocation.ConnectDeadlineAt)
	if _, err := f.store.AuthorizeLogin(context.Background(), allocation.RunID, allocation.CredentialToken); !errors.Is(err, ErrRunExpired) {
		t.Fatalf("login authorized at exact connect deadline: %v", err)
	}
}

func TestRequestStopRejectsCorruptRunStateWithoutReleasingOwnership(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	allocation, err := f.store.Allocate(context.Background(), allocationRequest("corrupt-stop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.HSet(context.Background(), f.store.runKey(allocation.RunID), "state", "unknown").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(context.Background(), allocation.RunID, allocation.CredentialToken); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("corrupt state was mutated as a valid stop: %v", err)
	}
	counts, err := readCounts(f, "installation-a", "192.0.2.15")
	if err != nil || counts.InstallationActive != 1 || counts.NetworkActive != 1 {
		t.Fatalf("corrupt state released ownership: %#v %v", counts, err)
	}
}

func TestAuthorizeAndMarkConnectedRequireRunningDesiredState(t *testing.T) {
	for _, operation := range []string{"authorize", "mark connected"} {
		t.Run(operation, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			allocation, err := f.store.Allocate(context.Background(), allocationRequest("desired-stopped"))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.client.HSet(context.Background(), f.store.runKey(allocation.RunID), "desired_state", "stopped").Err(); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "authorize":
				_, err = f.store.Authorize(context.Background(), allocation.RunID, allocation.CredentialToken, allocation.ProxyName)
			case "mark connected":
				_, err = f.store.MarkConnected(context.Background(), allocation.RunID, allocation.ProxyName)
			}
			if !errors.Is(err, ErrRunStopped) {
				t.Fatalf("%s ignored desired stopped state: %v", operation, err)
			}
		})
	}
}

func TestRunOperationsValidateImmutableRunIDBeforeMutation(t *testing.T) {
	for _, operation := range []string{"heartbeat", "stop"} {
		t.Run(operation, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			allocation, err := f.store.Allocate(context.Background(), allocationRequest("immutable-run-id"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.MarkConnected(context.Background(), allocation.RunID, allocation.ProxyName); err != nil {
				t.Fatal(err)
			}
			runKey := f.store.runKey(allocation.RunID)
			before, err := f.client.HMGet(context.Background(), runKey, "state", "desired_state", "lease_expires_at").Result()
			if err != nil {
				t.Fatal(err)
			}
			if err := f.client.HSet(context.Background(), runKey, "run_id", "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa").Err(); err != nil {
				t.Fatal(err)
			}
			f.clock.Set(f.clock.Now().Add(time.Second))
			switch operation {
			case "heartbeat":
				_, err = f.store.Heartbeat(context.Background(), allocation.RunID, allocation.CredentialToken)
			case "stop":
				_, err = f.store.RequestStop(context.Background(), allocation.RunID, allocation.CredentialToken)
			}
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("operation accepted mismatched stored run ID: %v", err)
			}
			after, err := f.client.HMGet(context.Background(), runKey, "state", "desired_state", "lease_expires_at").Result()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("operation mutated run before detecting identity mismatch: before=%v after=%v", before, after)
			}
		})
	}
}

func TestConfirmReleasedComparesCurrentResourceOwner(t *testing.T) {
	f := newRedisFixture(t, func(config *Config) {
		config.TCPPorts = []uint16{23001}
	})
	f.ready(t)
	firstReq := allocationRequest("old")
	firstReq.Protocol = ProtocolTCP
	first, err := f.store.Allocate(context.Background(), firstReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(context.Background(), first.RunID, first.CredentialToken); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(first.RunID, first.ProxyName)); err != nil {
		t.Fatal(err)
	}
	secondReq := allocationRequest("new")
	secondReq.InstallationID = "installation-new"
	secondReq.Protocol = ProtocolTCP
	second, err := f.store.Allocate(context.Background(), secondReq)
	if err != nil {
		t.Fatal(err)
	}
	// A delayed duplicate confirmation for the old run must not delete the
	// resource now owned by the new run.
	if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(first.RunID, first.ProxyName)); err != nil && !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := f.store.Authorize(context.Background(), second.RunID, second.CredentialToken, second.ProxyName); err != nil {
		t.Fatalf("late release removed new owner: %v", err)
	}
}

func TestConfirmReleasedRequiresOfflineAvailableZeroConnectionEvidence(t *testing.T) {
	zero := int64(0)
	for _, test := range []struct {
		name      string
		offline   bool
		available bool
		current   int64
	}{
		{name: "online zero", available: true, current: zero},
		{name: "offline unavailable", offline: true, current: zero},
		{name: "offline active connection", offline: true, available: true, current: 1},
		{name: "offline invalid connection count", offline: true, available: true, current: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			allocation, err := f.store.Allocate(context.Background(), allocationRequest("release-evidence"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.RequestStop(context.Background(), allocation.RunID, allocation.CredentialToken); err != nil {
				t.Fatal(err)
			}
			evidence := ReleaseEvidence{
				Kind: ReleaseEvidenceOfflineSample, RunID: allocation.RunID, ProxyName: allocation.ProxyName,
				ObservedOffline: test.offline, SampleAvailable: test.available, CurrentConnections: test.current,
			}
			if err := f.store.ConfirmReleased(context.Background(), evidence); !errors.Is(err, ErrReleaseUnconfirmed) {
				t.Fatalf("unsafe release evidence accepted: %#v %v", evidence, err)
			}
			counts, err := readCounts(f, "installation-a", "192.0.2.15")
			if err != nil || counts.InstallationActive != 1 || counts.NetworkActive != 1 {
				t.Fatalf("unconfirmed release freed ownership: %#v %v", counts, err)
			}
			f.clock.Set(f.clock.Now().Add(time.Minute))
			items, err := f.store.PendingVerification(context.Background(), 1)
			if err != nil || len(items) != 1 || items[0].RunID != allocation.RunID {
				t.Fatalf("unconfirmed release left verification queue: %#v %v", items, err)
			}
		})
	}
}

func TestConfirmReleasedAllowsOnlyProvenNeverRegisteredReservation(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	allocation, err := f.store.Allocate(ctx, allocationRequest("never-registered"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := ReleaseEvidence{
		Kind: ReleaseEvidenceNeverRegistered, RunID: allocation.RunID, ProxyName: allocation.ProxyName,
		ConfirmedNeverRegistered: true,
	}
	if err := f.store.ConfirmReleased(ctx, evidence); !errors.Is(err, ErrReleaseUnconfirmed) {
		t.Fatalf("reservation released before the complete registration window: %v", err)
	}
	f.clock.Set(allocation.ConnectDeadlineAt)
	if err := f.store.ConfirmReleased(ctx, evidence); err != nil {
		t.Fatalf("proven never-registered reservation was not released: %v", err)
	}

	connectedRequest := allocationRequest("once-connected")
	connectedRequest.InstallationID = "once-connected-installation"
	connectedRequest.NetworkKey = "198.51.100.91"
	connected, err := f.store.Allocate(ctx, connectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkConnected(ctx, connected.RunID, connected.ProxyName); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(ctx, connected.RunID, connected.CredentialToken); err != nil {
		t.Fatal(err)
	}
	connectedEvidence := ReleaseEvidence{
		Kind: ReleaseEvidenceNeverRegistered, RunID: connected.RunID, ProxyName: connected.ProxyName,
		ConfirmedNeverRegistered: true,
	}
	if err := f.store.ConfirmReleased(ctx, connectedEvidence); !errors.Is(err, ErrReleaseUnconfirmed) {
		t.Fatalf("once-connected run accepted never-registered evidence: %v", err)
	}
}

func TestConfirmReleasedValidatesStateBeforeDeletingExpiredOwnership(t *testing.T) {
	for _, test := range []struct {
		state   string
		desired string
	}{
		{"corrupt", "running"},
		{"stopping", "running"},
		{"verifying", "invalid"},
		{"online", "stopped"},
		{"released", "running"},
	} {
		t.Run(test.state+"/"+test.desired, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			allocation, err := f.store.Allocate(ctx, allocationRequest("invalid-release-state"))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.client.HSet(ctx, f.store.runKey(allocation.RunID), "state", test.state, "desired_state", test.desired).Err(); err != nil {
				t.Fatal(err)
			}
			f.clock.Set(allocation.HardExpiresAt)
			if err := f.store.ConfirmReleased(ctx, confirmedRelease(allocation.RunID, allocation.ProxyName)); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("corrupt expired state accepted for release: %v", err)
			}
			owner, err := f.client.Get(ctx, f.store.resourceKey(allocation.Protocol, allocation.PublicEndpoint)).Result()
			if err != nil || owner != allocation.RunID {
				t.Fatalf("corrupt expired state deleted resource ownership: owner=%q err=%v", owner, err)
			}
		})
	}
}

func TestRequestStopIsIdempotentForRetainedReleasedRun(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	allocation, err := f.store.Allocate(ctx, allocationRequest("released-stop-retry"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConfirmReleased(ctx, confirmedRelease(allocation.RunID, allocation.ProxyName)); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken)
	if err != nil || run.State != StateReleased || run.DesiredState != DesiredStopped {
		t.Fatalf("released stop retry was not idempotent: %#v %v", run, err)
	}
}

func TestConfirmedReleaseKeepsIdempotencyTombstoneWithoutRepeatingSuccessCount(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	request := allocationRequest("release-tombstone")
	first, err := f.store.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(context.Background(), first.RunID, first.CredentialToken); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(first.RunID, first.ProxyName)); err != nil {
		t.Fatal(err)
	}
	before, err := readCounts(f, request.InstallationID, request.NetworkKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.Allocate(context.Background(), request)
	if !errors.Is(err, ErrRunStopped) || !reflect.DeepEqual(got, Allocation{}) {
		t.Fatalf("released run was allocated again within replay window: %#v %v", got, err)
	}
	after, err := readCounts(f, request.InstallationID, request.NetworkKey)
	if err != nil || after != before || after.InstallationActive != 0 || after.NetworkActive != 0 || after.InstallationAllocations != 1 || after.NetworkAllocations != 1 {
		t.Fatalf("terminal replay changed occupancy or success count: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestConfirmReleasedRejectsCorruptDynamicKeysOutsideTheirNamespace(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	allocation, err := f.store.Allocate(context.Background(), allocationRequest("dynamic-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(context.Background(), allocation.RunID, allocation.CredentialToken); err != nil {
		t.Fatal(err)
	}
	sentinel := f.prefix + ":sentinel"
	if err := f.client.Set(context.Background(), sentinel, allocation.RunID, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := f.client.HSet(context.Background(), f.store.runKey(allocation.RunID), "resource_key", sentinel).Err(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(allocation.RunID, allocation.ProxyName)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("corrupt dynamic key accepted: %v", err)
	}
	if got, err := f.client.Get(context.Background(), sentinel).Result(); err != nil || got != allocation.RunID {
		t.Fatalf("out-of-namespace key was mutated: value=%q err=%v", got, err)
	}
	counts, err := readCounts(f, "installation-a", "192.0.2.15")
	if err != nil || counts.InstallationActive != 1 || counts.NetworkActive != 1 {
		t.Fatalf("corrupt dynamic key partially released ownership: %#v %v", counts, err)
	}
}

func TestConfirmReleasedRejectsAnotherRunsValidDynamicKeysBeforeAnyMutation(t *testing.T) {
	for _, field := range []string{"installation_active_key", "replay_key"} {
		t.Run(field, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			firstRequest := allocationRequest("dynamic-owner-first")
			first, err := f.store.Allocate(context.Background(), firstRequest)
			if err != nil {
				t.Fatal(err)
			}
			secondRequest := allocationRequest("dynamic-owner-second")
			secondRequest.InstallationID = "dynamic-owner-second-installation"
			secondRequest.NetworkKey = "198.51.100.62"
			second, err := f.store.Allocate(context.Background(), secondRequest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.RequestStop(context.Background(), first.RunID, first.CredentialToken); err != nil {
				t.Fatal(err)
			}
			var otherKey string
			switch field {
			case "installation_active_key":
				otherKey = f.store.installationActiveKey(secondRequest.InstallationID)
			case "replay_key":
				otherKey = f.store.replayKey(secondRequest.InstallationID, secondRequest.NetworkKey, secondRequest.IdempotencyKey)
			}
			if err := f.client.HSet(context.Background(), f.store.runKey(first.RunID), field, otherKey).Err(); err != nil {
				t.Fatal(err)
			}
			if err := f.store.ConfirmReleased(context.Background(), confirmedRelease(first.RunID, first.ProxyName)); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("another run's %s was accepted: %v", field, err)
			}
			if _, err := f.store.Authorize(context.Background(), second.RunID, second.CredentialToken, second.ProxyName); err != nil {
				t.Fatalf("other run was mutated: %v", err)
			}
			replayed, err := f.store.Allocate(context.Background(), secondRequest)
			if err != nil || !replayed.Replayed || replayed.CredentialToken != second.CredentialToken {
				t.Fatalf("other replay was mutated: %#v %v", replayed, err)
			}
			firstCounts, err := readCounts(f, firstRequest.InstallationID, firstRequest.NetworkKey)
			if err != nil || firstCounts.InstallationActive != 1 || firstCounts.NetworkActive != 1 {
				t.Fatalf("first ownership was partially released: %#v %v", firstCounts, err)
			}
		})
	}
}

func TestAllocateDoesNotOverwriteAnExistingProxyOwnerOnRandomCollision(t *testing.T) {
	stream := make([]byte, 0, 33*108)
	appendCandidateRandom := func(run, credential, secret, label, nonce byte) {
		stream = append(stream, bytes.Repeat([]byte{run}, 16)...)
		stream = append(stream, bytes.Repeat([]byte{0x42}, 16)...) // same proxy every attempt
		stream = append(stream, bytes.Repeat([]byte{credential}, 16)...)
		stream = append(stream, bytes.Repeat([]byte{secret}, 32)...)
		stream = append(stream, bytes.Repeat([]byte{label}, 16)...)
		stream = append(stream, bytes.Repeat([]byte{nonce}, 12)...)
	}
	appendCandidateRandom(1, 2, 3, 4, 5)
	for i := 0; i < maxResourceAttempts; i++ {
		appendCandidateRandom(byte(10+i), byte(50+i), byte(90+i), byte(130+i), byte(170+i))
	}
	f := newRedisFixture(t, nil)
	f.ready(t)
	f.store.random = bytes.NewReader(stream)
	first, err := f.store.Allocate(context.Background(), allocationRequest("proxy-owner-first"))
	if err != nil {
		t.Fatal(err)
	}
	request := allocationRequest("proxy-owner-second")
	request.InstallationID = "proxy-owner-second-installation"
	request.NetworkKey = "198.51.100.40"
	if got, err := f.store.Allocate(context.Background(), request); !errors.Is(err, ErrResourceUnavailable) || !reflect.DeepEqual(got, Allocation{}) {
		t.Fatalf("proxy collision overwrote the first owner: %#v %v", got, err)
	}
	if _, err := f.store.Authorize(context.Background(), first.RunID, first.CredentialToken, first.ProxyName); err != nil {
		t.Fatalf("first proxy owner was corrupted: %v", err)
	}
}

func TestPendingVerificationOrderingAndLimit(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	var allocations []Allocation
	for i := 0; i < 4; i++ {
		req := allocationRequest(fmt.Sprintf("verify-%d", i))
		req.InstallationID = fmt.Sprintf("verify-installation-%d", i)
		req.NetworkKey = fmt.Sprintf("192.0.2.%d", 100+i)
		got, err := f.store.Allocate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		allocations = append(allocations, got)
		if _, err := f.store.RequestStop(context.Background(), got.RunID, got.CredentialToken); err != nil {
			t.Fatal(err)
		}
		f.clock.Set(f.clock.Now().Add(time.Millisecond))
	}
	items, err := f.store.PendingVerification(context.Background(), 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("pending verification=%#v %v", items, err)
	}
	got := []string{items[0].RunID, items[1].RunID}
	want := []string{allocations[0].RunID, allocations[1].RunID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordering=%v want=%v", got, want)
	}
	next, err := f.store.PendingVerification(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(next))
	for _, item := range next {
		ids = append(ids, item.RunID)
	}
	sort.Strings(ids)
	want = []string{allocations[2].RunID, allocations[3].RunID}
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("fixed head starved later verification work: got=%v want=%v", ids, want)
	}
	f.clock.Set(f.clock.Now().Add(time.Minute))
	retried, err := f.store.PendingVerification(context.Background(), 4)
	if err != nil || len(retried) != 4 {
		t.Fatalf("abandoned claims were not made eligible again: %#v %v", retried, err)
	}
	ids = ids[:0]
	for _, item := range retried {
		ids = append(ids, item.RunID)
	}
	sort.Strings(ids)
	want = want[:0]
	for _, allocation := range allocations {
		want = append(want, allocation.RunID)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("abandoned claims were not all retried: got=%v want=%v", ids, want)
	}
}

func TestPendingVerificationQuarantinesCorruptionAndContinuesPastLimit(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	first, err := f.store.Allocate(ctx, allocationRequest("corrupt-verification-first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(ctx, first.RunID, first.CredentialToken); err != nil {
		t.Fatal(err)
	}
	f.clock.Set(f.clock.Now().Add(time.Millisecond))
	secondRequest := allocationRequest("corrupt-verification-second")
	secondRequest.InstallationID = "second-verification-installation"
	secondRequest.NetworkKey = "198.51.100.90"
	second, err := f.store.Allocate(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(ctx, second.RunID, second.CredentialToken); err != nil {
		t.Fatal(err)
	}
	firstKey := f.store.runKey(first.RunID)
	if err := f.client.HDel(ctx, firstKey, "proxy_name").Err(); err != nil {
		t.Fatal(err)
	}
	items, err := f.store.PendingVerification(ctx, 1)
	if !errors.Is(err, ErrVerificationCorrupt) || len(items) != 1 || items[0].RunID != second.RunID {
		t.Fatalf("corrupt head starved trusted later work: items=%#v err=%v", items, err)
	}
	if _, err := f.client.ZScore(ctx, f.store.verificationKey(), firstKey).Result(); !errors.Is(err, redis.Nil) {
		t.Fatalf("corrupt item remains in active queue: %v", err)
	}
	if _, err := f.client.ZScore(ctx, f.store.verificationQuarantineKey(), firstKey).Result(); err != nil {
		t.Fatalf("corrupt item was not quarantined: %v", err)
	}
	if _, err := f.store.Allocate(ctx, allocationRequest("blocked-after-corruption")); !errors.Is(err, ErrResourcesUnverified) {
		t.Fatalf("corruption did not block new allocations: %v", err)
	}
	counts, err := readCounts(f, "installation-a", "192.0.2.15")
	if err != nil || counts.InstallationActive != 1 || counts.NetworkActive != 1 {
		t.Fatalf("corrupt unknown ownership was silently released: %#v %v", counts, err)
	}
	if err := f.store.ConfirmReleased(ctx, confirmedRelease(second.RunID, second.ProxyName)); err != nil {
		t.Fatalf("trusted later item could not be released while allocations blocked: %v", err)
	}
}

func TestPendingVerificationBoundsCorruptWorkAndAdvancesAcrossBatches(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	allocation, err := f.store.Allocate(ctx, allocationRequest("verification-budget"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := f.client.ZAdd(ctx, f.store.verificationKey(), redis.Z{
			Score: float64(f.clock.Now().UnixMilli() - int64(3-index)), Member: fmt.Sprintf("%s:run:invalid-%d", f.prefix, index),
		}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	items, err := f.store.PendingVerification(ctx, 1)
	var corruption *VerificationCorruptionError
	if len(items) != 0 || !errors.As(err, &corruption) || corruption.Count != 2 {
		t.Fatalf("batch ignored its corruption examination budget: items=%#v err=%v", items, err)
	}
	if remaining, err := f.client.ZCard(ctx, f.store.verificationKey()).Result(); err != nil || remaining != 2 {
		t.Fatalf("first batch examined beyond its bounded work: remaining=%d err=%v", remaining, err)
	}
	items, err = f.store.PendingVerification(ctx, 1)
	if !errors.As(err, &corruption) || corruption.Count != 1 || len(items) != 1 || items[0].RunID != allocation.RunID {
		t.Fatalf("corrupt items starved valid work across batches: items=%#v err=%v", items, err)
	}
}

func TestPendingVerificationDoesNotQuarantineRenewedClaim(t *testing.T) {
	for _, heartbeatDelay := range []time.Duration{time.Second, 75 * time.Second} {
		t.Run(heartbeatDelay.String(), func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			allocation, err := f.store.Allocate(ctx, allocationRequest("verification-renewal"))
			if err != nil {
				t.Fatal(err)
			}
			run, err := f.store.MarkConnected(ctx, allocation.RunID, allocation.ProxyName)
			if err != nil {
				t.Fatal(err)
			}
			verificationTime := run.LeaseExpiresAt
			f.clock.Set(verificationTime)
			f.client.AddHook(&beforeVerificationInspectionHook{run: func() error {
				// Model an in-flight heartbeat whose clock was captured before the
				// worker claimed the old deadline. The second case equals the claim score.
				f.clock.Set(verificationTime.Add(-heartbeatDelay))
				_, err := f.store.Heartbeat(ctx, allocation.RunID, allocation.CredentialToken)
				f.clock.Set(verificationTime)
				return err
			}})
			items, err := f.store.PendingVerification(ctx, 1)
			if err != nil || len(items) != 0 {
				t.Fatalf("legitimate renewal was returned or quarantined as corruption: items=%#v err=%v", items, err)
			}
			if count, err := f.client.ZCard(ctx, f.store.verificationQuarantineKey()).Result(); err != nil || count != 0 {
				t.Fatalf("renewed run was quarantined: count=%d err=%v", count, err)
			}
			if _, err := f.store.Authorize(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); err != nil {
				t.Fatalf("claim race blocked valid authorization: %v", err)
			}
		})
	}
}

func TestVerificationEndpointMustMatchAllocatedProtocolNamespace(t *testing.T) {
	store := &Store{publicDomain: "tunnel.test", tcpPorts: []uint16{21001}, udpPorts: []uint16{22001}}
	for _, test := range []struct {
		protocol Protocol
		endpoint string
		want     bool
	}{
		{ProtocolHTTP, "anon-aaaaaaaaaaaaaaaaaaaaaaaaaa.tunnel.test", true},
		{ProtocolTCP, "tunnel.test:21001", true},
		{ProtocolUDP, "tunnel.test:22001", true},
		{ProtocolHTTP, "permanent.tunnel.test", false},
		{ProtocolHTTP, "anon-a.tunnel.test", false},
		{ProtocolHTTP, "anon-aaaaaaaaaaaaaaaaaaaaaaaaaa.other.test", false},
		{ProtocolTCP, "tunnel.test:22001", false},
		{ProtocolUDP, "tunnel.test:21001", false},
		{ProtocolTCP, "tunnel.test:021001", false},
		{ProtocolTCP, "other.test:21001", false},
		{Protocol("invalid"), "tunnel.test:21001", false},
	} {
		if got := store.validPublicEndpoint(test.protocol, test.endpoint); got != test.want {
			t.Fatalf("endpoint %q for %s: valid=%t want=%t", test.endpoint, test.protocol, got, test.want)
		}
	}
}

func TestConstructorRejectsEmptyProtocolPools(t *testing.T) {
	f := newRedisFixture(t, nil)
	base := Config{
		Client: f.client, Prefix: f.prefix + ":pool",
		CredentialPepper: []byte("credential-pepper-is-independent-32"),
		ReplayKey:        []byte("replay-key-is-exactly-32-bytes!!"),
		FenceOwnerToken:  []byte("fence-owner-token-is-independent-32"),
		Clock:            f.clock.Now, Random: rand.Reader, PublicDomain: "tunnel.test",
		TCPPorts: []uint16{21001}, UDPPorts: []uint16{22001},
	}
	for _, protocol := range []Protocol{ProtocolTCP, ProtocolUDP} {
		config := base
		if protocol == ProtocolTCP {
			config.TCPPorts = nil
		} else {
			config.UDPPorts = nil
		}
		if _, err := NewStore(config); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("empty %s pool accepted: %v", protocol, err)
		}
	}
}

func TestNetworkKeyRejectsSpecialIPv6Prefixes(t *testing.T) {
	for _, value := range []string{"::/64", "ff00::/64", "fe80::/64"} {
		request := allocationRequest("invalid-ipv6")
		request.NetworkKey = value
		if _, err := hashAllocationRequest(request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("special IPv6 prefix accepted: %q err=%v", value, err)
		}
	}
	request := allocationRequest("valid-ipv6")
	request.NetworkKey = "2001:db8::/64"
	if _, err := hashAllocationRequest(request); err != nil {
		t.Fatalf("canonical IPv6 /64 rejected: %v", err)
	}
}

func TestConstructorRejectsSharedKeysAndInvalidInputsDoNotTouchRedis(t *testing.T) {
	f := newRedisFixture(t, nil)
	shared := []byte("shared-purpose-key-is-32-bytes!!")
	if len(shared) != 32 {
		t.Fatal("test key length")
	}
	_, err := NewStore(Config{
		Client: f.client, Prefix: f.prefix + ":other", CredentialPepper: shared, ReplayKey: shared,
		FenceOwnerToken: []byte("fence-owner-token-is-independent-32"), PublicDomain: "tunnel.test",
		Random: rand.Reader, TCPPorts: []uint16{1}, UDPPorts: []uint16{2},
	})
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("shared HMAC/AEAD key accepted: %v", err)
	}
	f.ready(t)
	for _, mutate := range []func(*AllocateRequest){
		func(r *AllocateRequest) { r.InstallationID = "" },
		func(r *AllocateRequest) { r.NetworkKey = "" },
		func(r *AllocateRequest) { r.IdempotencyKey = "" },
		func(r *AllocateRequest) { r.Protocol = "ftp" },
		func(r *AllocateRequest) { r.LocalHost = "" },
		func(r *AllocateRequest) { r.LocalPort = 0 },
	} {
		req := allocationRequest("invalid")
		mutate(&req)
		if got, err := f.store.Allocate(context.Background(), req); !errors.Is(err, ErrInvalidRequest) || !reflect.DeepEqual(got, Allocation{}) {
			t.Fatalf("invalid request result=%#v err=%v", got, err)
		}
	}
}

func validRandomName(value, prefix string, bytes int) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	return validEncodedRandom(strings.TrimPrefix(value, prefix), bytes)
}

func validEncodedRandom(value string, bytes int) bool {
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	decoded, err := encoding.DecodeString(strings.ToUpper(value))
	return err == nil && value == strings.ToLower(value) && len(decoded) == bytes && strings.ToLower(encoding.EncodeToString(decoded)) == value
}

func parseTestCredential(token string) (string, []byte, bool) {
	id, encoded, ok := strings.Cut(token, ".")
	if !ok {
		return "", nil, false
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(secret) != encoded {
		return "", nil, false
	}
	return id, secret, true
}

func confirmedRelease(runID, proxyName string) ReleaseEvidence {
	return ReleaseEvidence{
		Kind: ReleaseEvidenceOfflineSample, RunID: runID, ProxyName: proxyName, ObservedOffline: true,
		SampleAvailable: true, CurrentConnections: 0,
	}
}

type testCounts struct {
	InstallationActive      int64
	NetworkActive           int64
	InstallationAllocations int64
	NetworkAllocations      int64
}

func readCounts(f *redisFixture, installationID, networkKey string) (testCounts, error) {
	ctx := context.Background()
	pipe := f.client.Pipeline()
	installationActive := pipe.ZCard(ctx, f.store.installationActiveKey(installationID))
	networkActive := pipe.ZCard(ctx, f.store.networkActiveKey(networkKey))
	installationRate := pipe.ZCard(ctx, f.store.installationRateKey(installationID))
	networkRate := pipe.ZCard(ctx, f.store.networkRateKey(networkKey))
	if _, err := pipe.Exec(ctx); err != nil {
		return testCounts{}, err
	}
	return testCounts{
		InstallationActive:      installationActive.Val(),
		NetworkActive:           networkActive.Val(),
		InstallationAllocations: installationRate.Val(),
		NetworkAllocations:      networkRate.Val(),
	}, nil
}
