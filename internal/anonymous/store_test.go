package anonymous

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
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

func newRedisFixture(t *testing.T, mutate func(*Config)) *redisFixture {
	t.Helper()
	url := os.Getenv("NODELANE_TEST_REDIS_URL")
	if url == "" {
		t.Skip("NODELANE_TEST_REDIS_URL is required for real Redis tests")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("test Redis unavailable: %v", err)
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
	if err := f.store.MarkResourcesVerified(context.Background()); err != nil {
		t.Fatal(err)
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
	if err := f.store.ConfirmReleased(context.Background(), first.RunID, first.ProxyName); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Allocate(context.Background(), other); err != nil {
		t.Fatalf("confirmed release did not free active slot: %v", err)
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
		if err := f.store.ConfirmReleased(context.Background(), got.RunID, got.ProxyName); err != nil {
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
		if err := f.store.ConfirmReleased(context.Background(), got.RunID, got.ProxyName); err != nil {
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
	if err := f.store.ConfirmReleased(context.Background(), first.RunID, first.ProxyName); err != nil {
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
	if err := f.store.ConfirmReleased(context.Background(), first.RunID, first.ProxyName); err != nil && !errors.Is(err, ErrRunNotFound) {
		t.Fatal(err)
	}
	if _, err := f.store.Authorize(context.Background(), second.RunID, second.CredentialToken, second.ProxyName); err != nil {
		t.Fatalf("late release removed new owner: %v", err)
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
	if err := f.store.ConfirmReleased(context.Background(), first.RunID, first.ProxyName); err != nil {
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
	if err := f.store.ConfirmReleased(context.Background(), allocation.RunID, allocation.ProxyName); !errors.Is(err, ErrInvalidState) {
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
			if err := f.store.ConfirmReleased(context.Background(), first.RunID, first.ProxyName); !errors.Is(err, ErrInvalidState) {
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
	f := newRedisFixture(t, func(config *Config) { config.Random = bytes.NewReader(stream) })
	f.ready(t)
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
	for i := 0; i < 3; i++ {
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
	all, err := f.store.PendingVerification(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(all))
	for _, item := range all {
		ids = append(ids, item.RunID)
	}
	sort.Strings(ids)
	want = []string{allocations[0].RunID, allocations[1].RunID, allocations[2].RunID}
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("verification index=%v want=%v", ids, want)
	}
}

func TestConstructorRejectsSharedKeysAndInvalidInputsDoNotTouchRedis(t *testing.T) {
	f := newRedisFixture(t, nil)
	shared := []byte("shared-purpose-key-is-32-bytes!!")
	if len(shared) != 32 {
		t.Fatal("test key length")
	}
	_, err := NewStore(Config{Client: f.client, Prefix: f.prefix + ":other", CredentialPepper: shared, ReplayKey: shared, PublicDomain: "tunnel.test", Random: rand.Reader})
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

type testCounts struct {
	InstallationActive      int64
	NetworkActive           int64
	InstallationAllocations int64
	NetworkAllocations      int64
}

func readCounts(f *redisFixture, installationID, networkKey string) (testCounts, error) {
	ctx := context.Background()
	pipe := f.client.Pipeline()
	installationActive := pipe.SCard(ctx, f.store.installationActiveKey(installationID))
	networkActive := pipe.SCard(ctx, f.store.networkActiveKey(networkKey))
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
