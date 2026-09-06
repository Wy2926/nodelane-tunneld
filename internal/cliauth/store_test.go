package cliauth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	mu                  sync.Mutex
	values              map[string]string
	err                 error
	calls               int
	noOpSet, noOpDelete bool
	getErr, deleteErr   error
}

func (k *fakeKeyring) Get(service, account string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls++
	if k.err != nil {
		return "", k.err
	}
	if k.getErr != nil {
		return "", k.getErr
	}
	value, ok := k.values[service+"/"+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (k *fakeKeyring) Set(service, account, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls++
	if k.err != nil {
		return k.err
	}
	if k.noOpSet {
		return nil
	}
	if k.values == nil {
		k.values = make(map[string]string)
	}
	k.values[service+"/"+account] = value
	return nil
}
func (k *fakeKeyring) Delete(service, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls++
	if k.err != nil {
		return k.err
	}
	if k.deleteErr != nil {
		return k.deleteErr
	}
	if k.noOpDelete {
		return nil
	}
	if _, ok := k.values[service+"/"+account]; !ok {
		return keyring.ErrNotFound
	}
	delete(k.values, service+"/"+account)
	return nil
}

func storeFixtureCredentials() Credentials {
	return Credentials{Issuer: "https://auth.nodelane.net/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "test-refresh-secret"}
}

func newTestSystemStore(t *testing.T, backend *fakeKeyring, options SystemStoreOptions) Store {
	t.Helper()
	if options.Service == "" {
		options.Service = "nodelane-nt-test-" + t.Name()
	}
	if options.Account == "" {
		options.Account = "test-account"
	}
	if options.LockDirectory == "" {
		options.LockDirectory = filepath.Join(t.TempDir(), "locks")
	}
	options.Backend = backend
	store, err := NewSystemStore(options)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSystemStoreConstructionIsLazyAndPersistsOnlyCredentials(t *testing.T) {
	t.Parallel()
	backend := &fakeKeyring{}
	lockDirectory := filepath.Join(t.TempDir(), "not-created")
	store := newTestSystemStore(t, backend, SystemStoreOptions{LockDirectory: lockDirectory})
	if backend.calls != 0 {
		t.Fatal("constructor accessed keyring")
	}
	if _, err := os.Stat(lockDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor created lock directory")
	}
	ctx := context.Background()
	if _, err := store.Load(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("missing credentials: %v", err)
	}
	want := storeFixtureCredentials()
	if err := store.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx)
	if err != nil || got != want {
		t.Fatalf("keyring roundtrip failed: %v", err)
	}
	for _, encoded := range backend.values {
		var object map[string]any
		if err := json.Unmarshal([]byte(encoded), &object); err != nil {
			t.Fatal(err)
		}
		if len(object) != 4 || object["refresh_token"] != "test-refresh-secret" || object["issuer"] != "https://auth.nodelane.net/oidc" || object["client_id"] != "native-client" || object["resource"] != testResource {
			t.Fatal("unexpected persisted credential payload")
		}
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
}

func TestSystemStoreSanitizesBackendFailuresWithoutFallback(t *testing.T) {
	t.Parallel()
	backend := &fakeKeyring{err: errors.New("LEAK-keyring-password")}
	store := newTestSystemStore(t, backend, SystemStoreOptions{})
	_, loadErr := store.Load(context.Background())
	for _, err := range []error{loadErr, store.Save(context.Background(), storeFixtureCredentials()), store.Delete(context.Background())} {
		if !errors.Is(err, ErrCredentialsUnavailable) || strings.Contains(err.Error(), "LEAK") {
			t.Fatalf("keyring failure was hidden or leaked: %v", err)
		}
	}
	if len(backend.values) != 0 {
		t.Fatal("failed keyring wrote credentials")
	}
}

func TestSystemStoreRejectsCorruptOrOversizeRecords(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{`{}`, `{"issuer":"https://auth.nodelane.net/oidc","client_id":"native-client","resource":"https://tunnel.nodelane.net/api","refresh_token":"secret","access_token":"unexpected"}`, strings.Repeat("x", 70<<10)} {
		backend := &fakeKeyring{values: map[string]string{"test-service/test-account": payload}}
		store := newTestSystemStore(t, backend, SystemStoreOptions{Service: "test-service"})
		if _, err := store.Load(context.Background()); !errors.Is(err, ErrCredentialsUnavailable) {
			t.Fatalf("corrupt record accepted: %v", err)
		}
	}
}

func TestSystemTransactionsSerializeStoreInstancesAndRespectCancellation(t *testing.T) {
	t.Parallel()
	backend := &fakeKeyring{}
	options := SystemStoreOptions{Service: "nodelane-transaction-" + t.Name(), LockDirectory: filepath.Join(t.TempDir(), "locks")}
	first := newTestSystemStore(t, backend, options).(TransactionalStore)
	second := newTestSystemStore(t, backend, options).(TransactionalStore)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFirst()
	go func() {
		finished <- first.Transaction(context.Background(), func(Store) error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("first lock failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first lock timeout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := second.Transaction(ctx, func(Store) error { t.Error("transaction entered while other store held lock"); return nil })
	releaseFirst()
	if firstErr := <-finished; firstErr != nil {
		t.Fatal(firstErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation lost: %v", err)
	}
	if err := second.Save(context.Background(), storeFixtureCredentials()); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}

func TestSystemTransactionExcludesAnotherProcess(t *testing.T) {
	if os.Getenv("NODELANE_CLIAUTH_LOCK_CHILD") == "1" {
		store, err := NewSystemStore(SystemStoreOptions{Service: os.Getenv("NODELANE_CLIAUTH_LOCK_SERVICE"), Account: "process-test", LockDirectory: os.Getenv("NODELANE_CLIAUTH_LOCK_DIRECTORY"), Backend: &fakeKeyring{}})
		if err != nil {
			t.Fatal(err)
		}
		err = store.(TransactionalStore).Transaction(context.Background(), func(Store) error {
			fmt.Println("LOCKED")
			_, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
			return readErr
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Parallel()
	options := SystemStoreOptions{Service: fmt.Sprintf("nodelane-process-%d-%d", os.Getpid(), time.Now().UnixNano()), Account: "process-test", LockDirectory: filepath.Join(t.TempDir(), "locks")}
	store := newTestSystemStore(t, &fakeKeyring{}, options).(TransactionalStore)
	command := exec.Command(os.Args[0], "-test.run=^TestSystemTransactionExcludesAnotherProcess$")
	command.Env = append(os.Environ(), "NODELANE_CLIAUTH_LOCK_CHILD=1", "NODELANE_CLIAUTH_LOCK_SERVICE="+options.Service, "NODELANE_CLIAUTH_LOCK_DIRECTORY="+options.LockDirectory)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(line) }()
	select {
	case line := <-ready:
		if line != "LOCKED" {
			t.Fatalf("child did not acquire lock: %s", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child lock timeout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err = store.Transaction(ctx, func(Store) error { t.Error("cross-process lock was bypassed"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cross-process exclusion: %v", err)
	}
	_, _ = fmt.Fprintln(stdin, "release")
	if err = command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(context.Background(), storeFixtureCredentials()); err != nil {
		t.Fatalf("child lock not released: %v", err)
	}
}

func TestFileStoreRejectsRelativePathsAndWindowsPlaintextFallback(t *testing.T) {
	t.Parallel()
	if _, err := NewFileStore("credentials.json"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("relative credential path accepted: %v", err)
	}
	if runtime.GOOS == "windows" {
		if _, err := NewFileStore(filepath.Join(t.TempDir(), "credentials.json")); !errors.Is(err, ErrFileStoreUnsupported) {
			t.Fatalf("unsafe Windows fallback accepted: %v", err)
		}
	}
}

func TestSystemStoreRejectsAmbiguousNamespaces(t *testing.T) {
	t.Parallel()
	for _, options := range []SystemStoreOptions{{Service: "service:account", Account: "other"}, {Service: "service", Account: "account:other"}} {
		options.Backend = &fakeKeyring{}
		if _, err := NewSystemStore(options); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("ambiguous keyring namespace accepted: %v", err)
		}
	}
}

func TestSystemStoreCanonicalNamespaceSharesCredentialsAndLock(t *testing.T) {
	t.Parallel()
	backend := &fakeKeyring{}
	directory := filepath.Join(t.TempDir(), "locks")
	lower := newTestSystemStore(t, backend, SystemStoreOptions{Service: "nodelane-case", Account: "native-account", LockDirectory: directory})
	upper := newTestSystemStore(t, backend, SystemStoreOptions{Service: "NODELANE-CASE", Account: "NATIVE-ACCOUNT", LockDirectory: directory})
	if err := lower.Save(context.Background(), storeFixtureCredentials()); err != nil {
		t.Fatal(err)
	}
	if got, err := upper.Load(context.Background()); err != nil || got != storeFixtureCredentials() {
		t.Fatalf("case-aliased keyring namespace diverged: %v", err)
	}
}

func TestSystemStoreRequiresVerifiedSave(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"new-record", "rotation", "readback-failure"} {
		t.Run(test, func(t *testing.T) {
			backend := &fakeKeyring{}
			store := newTestSystemStore(t, backend, SystemStoreOptions{})
			old := storeFixtureCredentials()
			if test != "new-record" {
				if err := store.Save(context.Background(), old); err != nil {
					t.Fatal(err)
				}
			}
			backend.noOpSet = true
			if test == "readback-failure" {
				backend.noOpSet = false
				backend.getErr = errors.New("LEAK-readback-secret")
			}
			next := old
			next.RefreshToken = "rotated-refresh-secret"
			if err := store.Save(context.Background(), next); !errors.Is(err, ErrCredentialsUnavailable) || strings.Contains(err.Error(), "LEAK") {
				t.Fatalf("unverified save reported success or leaked: %v", err)
			}
			if test == "rotation" {
				if got, err := store.Load(context.Background()); err != nil || got != old {
					t.Fatalf("no-op save changed prior credentials: %v", err)
				}
			}
		})
	}
}

func TestSystemStoreRequiresVerifiedDeletion(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"no-op", "false-not-found", "readback-failure"} {
		t.Run(test, func(t *testing.T) {
			backend := &fakeKeyring{}
			store := newTestSystemStore(t, backend, SystemStoreOptions{})
			if err := store.Save(context.Background(), storeFixtureCredentials()); err != nil {
				t.Fatal(err)
			}
			switch test {
			case "no-op":
				backend.noOpDelete = true
			case "false-not-found":
				backend.deleteErr = keyring.ErrNotFound
			case "readback-failure":
				backend.getErr = errors.New("LEAK-delete-secret")
			}
			if err := store.Delete(context.Background()); !errors.Is(err, ErrCredentialsUnavailable) || strings.Contains(err.Error(), "LEAK") {
				t.Fatalf("unverified deletion reported success or leaked: %v", err)
			}
		})
	}
}

func TestClientDoesNotCacheAccessBeforeVerifiedKeyringSave(t *testing.T) {
	t.Parallel()
	f := newAuthFixture(t)
	backend := &fakeKeyring{}
	store := newTestSystemStore(t, backend, SystemStoreOptions{})
	old := Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "previous-refresh"}
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	client, err := New(context.Background(), Options{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, HTTPClient: f.server.Client(), Store: store})
	if err != nil {
		t.Fatal(err)
	}
	backend.noOpSet = true
	for range 2 {
		token, err := client.AccessToken(context.Background())
		if token != "" || !errors.Is(err, ErrCredentialsUnavailable) {
			t.Fatalf("access escaped unverified persistence: %v", err)
		}
	}
	if f.tokenCalls.Load() != 1 {
		t.Fatal("unverified pending rotation reused an old refresh token")
	}
	backend.noOpSet = false
	if token, err := client.AccessToken(context.Background()); err != nil || token != "access-secret" {
		t.Fatalf("verified retry failed: %v", err)
	}
	if got, err := store.Load(context.Background()); err != nil || got.RefreshToken != "refresh-rotated" {
		t.Fatalf("rotation was not durably stored: %v", err)
	}
}

func TestLoginAndLogoutDoNotTrustNoOpKeyringMutations(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"login", "logout"} {
		t.Run(test, func(t *testing.T) {
			f := newAuthFixture(t)
			backend := &fakeKeyring{}
			store := newTestSystemStore(t, backend, SystemStoreOptions{})
			old := Credentials{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, RefreshToken: "previous-refresh"}
			if test == "logout" {
				old.RefreshToken = "refresh-rotated"
			}
			if err := store.Save(context.Background(), old); err != nil {
				t.Fatal(err)
			}
			client, err := New(context.Background(), Options{Issuer: f.server.URL + "/oidc", ClientID: "native-client", Resource: testResource, HTTPClient: f.server.Client(), Store: store})
			if err != nil {
				t.Fatal(err)
			}
			if test == "login" {
				backend.noOpSet = true
				err = client.Login(context.Background(), func(DeviceCode) error { return nil })
			} else {
				backend.noOpDelete = true
				err = client.Logout(context.Background())
			}
			if !errors.Is(err, ErrCredentialsUnavailable) {
				t.Fatalf("%s falsely confirmed local mutation: %v", test, err)
			}
			if got, err := store.Load(context.Background()); err != nil || got != old {
				t.Fatalf("no-op mutation changed previous credentials: %v", err)
			}
		})
	}
}
