package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

func loginAt(now time.Time, state string) session.LoginTransaction {
	return session.LoginTransaction{State: state, Nonce: "nonce-secret-one", Verifier: "pkce-verifier-secret-one", Binding: "browser-binding-secret-one", ReturnTo: "/console/routes?source=login", Locale: "en", ExpiresAt: now.Add(5 * time.Minute)}
}

func TestLoginJSONExcludesSecrets(t *testing.T) {
	login := loginAt(time.Now(), "state-secret-one")
	data, err := json.Marshal(login)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{login.State, login.Nonce, login.Verifier, login.Binding} {
		if strings.Contains(string(data), secret) {
			t.Fatal("login JSON leaked credential material")
		}
	}
}

func TestLoginWrongBindingDoesNotConsumeAndFlowKeysAreIndependent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := loginAt(f.clock.Now(), "state-secret-one")
	second := loginAt(f.clock.Now(), "state-secret-two")
	for _, login := range []session.LoginTransaction{first, second} {
		if err := f.store.PutLogin(ctx, login); err != nil {
			t.Fatal(err)
		}
	}
	requireError(t, f.store.PutLogin(ctx, first), session.ErrConflict)
	got, err := f.store.ConsumeLogin(ctx, first.State, "wrong-browser-binding")
	requireError(t, err, session.ErrNotFound)
	if !reflect.DeepEqual(got, session.LoginTransaction{}) {
		t.Fatal("wrong binding returned partial secrets")
	}
	for _, login := range []session.LoginTransaction{first, second} {
		got, err := f.store.ConsumeLogin(ctx, login.State, login.Binding)
		if err != nil || !reflect.DeepEqual(got, login) {
			t.Fatalf("valid login could not be consumed: %v", err)
		}
		_, err = f.store.ConsumeLogin(ctx, login.State, login.Binding)
		requireError(t, err, session.ErrNotFound)
	}
}

func TestLoginConsumeIsAtomicAcrossConcurrentCallers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	login := loginAt(f.clock.Now(), "state-secret-concurrent")
	if err := f.store.PutLogin(ctx, login); err != nil {
		t.Fatal(err)
	}
	const callers = 16
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	type result struct {
		login session.LoginTransaction
		err   error
	}
	results := make(chan result, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			got, err := f.store.ConsumeLogin(ctx, login.State, login.Binding)
			results <- result{login: got, err: err}
		}()
	}
	ready.Wait()
	close(start)
	winners := 0
	for range callers {
		got := <-results
		if got.err == nil {
			winners++
			if !reflect.DeepEqual(got.login, login) {
				t.Error("winning consume did not preserve login payload")
			}
		} else if !errors.Is(got.err, session.ErrNotFound) {
			t.Errorf("losing consume error = %v", got.err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful concurrent consumes = %d, want 1", winners)
	}
}

func TestLoginStorageEncryptsSecretsAndRejectsAuthenticatedMutations(t *testing.T) {
	for _, mutation := range []string{"wrong-key", "data", "binding", "row-swap"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			login := loginAt(f.clock.Now(), "state-secret-one")
			if err := f.store.PutLogin(ctx, login); err != nil {
				t.Fatal(err)
			}
			key := hashedKey(f.prefix, "login", login.State)
			row, err := f.client.HGetAll(ctx, key).Result()
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{login.State, login.Nonce, login.Verifier, login.Binding, login.ReturnTo} {
				for name, value := range row {
					if strings.Contains(name, secret) || strings.Contains(value, secret) || strings.Contains(key, secret) {
						t.Fatal("Redis login storage leaked plaintext material")
					}
				}
			}
			ttl, err := f.client.PTTL(ctx, key).Result()
			if err != nil || ttl <= 0 || ttl > 5*time.Minute {
				t.Fatalf("login TTL = %v, err = %v", ttl, err)
			}
			reader := f.store
			switch mutation {
			case "wrong-key":
				key := append([]byte(nil), f.key...)
				key[0] ^= 1
				reader, err = session.NewRedisStore(f.client, f.prefix, key, f.clock.Now)
			case "data":
				data := []byte(row["data"])
				if len(data) == 0 {
					t.Fatal("login ciphertext missing")
				}
				data[len(data)-1] ^= 1
				err = f.client.HSet(ctx, key, "data", data).Err()
			case "binding":
				row["binding"] = strings.TrimPrefix(hashedKey("unused", "binding", "forged-binding"), "unused:binding:")
				err = f.client.HSet(ctx, key, "binding", row["binding"]).Err()
				login.Binding = "forged-binding"
			case "row-swap":
				other := loginAt(f.clock.Now(), "state-secret-two")
				if err = f.store.PutLogin(ctx, other); err != nil {
					t.Fatal(err)
				}
				err = f.client.HSet(ctx, hashedKey(f.prefix, "login", other.State), "data", row["data"]).Err()
				login = other
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := reader.ConsumeLogin(ctx, login.State, login.Binding)
			requireError(t, err, session.ErrInvalid)
			if !reflect.DeepEqual(got, session.LoginTransaction{}) {
				t.Fatal("corrupt login returned partial credentials")
			}
		})
	}
}

func TestLoginChecksLogicalExpiryAndRejectsLongOrInvalidTransactions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, invalid := range []string{"state", "nonce", "verifier", "binding", "expired", "too-long"} {
		login := loginAt(f.clock.Now(), "state-invalid")
		want := session.ErrInvalid
		switch invalid {
		case "state":
			login.State = ""
		case "nonce":
			login.Nonce = ""
		case "verifier":
			login.Verifier = ""
		case "binding":
			login.Binding = ""
		case "expired":
			login.ExpiresAt = f.clock.Now()
			want = session.ErrExpired
		case "too-long":
			login.ExpiresAt = f.clock.Now().Add(5*time.Minute + time.Millisecond)
		}
		requireError(t, f.store.PutLogin(ctx, login), want)
	}
	if keys := prefixKeys(t, f.client, f.prefix); len(keys) != 0 {
		t.Fatal("invalid login wrote Redis keys")
	}
	login := loginAt(f.clock.Now(), "state-expiring")
	if err := f.store.PutLogin(ctx, login); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Persist(ctx, hashedKey(f.prefix, "login", login.State)).Err(); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(5 * time.Minute)
	got, err := f.store.ConsumeLogin(ctx, login.State, login.Binding)
	requireError(t, err, session.ErrExpired)
	if !reflect.DeepEqual(got, session.LoginTransaction{}) {
		t.Fatal("expired login returned partial credentials")
	}
}
