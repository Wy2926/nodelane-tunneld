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

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

func refreshedTokens(now time.Time) identity.OIDCTokens {
	return identity.OIDCTokens{
		AccessToken:          "access-token-secret-two",
		RefreshToken:         "refresh-token-secret-two",
		IDToken:              "id-token-secret-two",
		AccessTokenExpiresAt: now.Add(2 * time.Hour),
		Identity: identity.OIDCIdentity{
			Issuer: "https://issuer.invalid", Subject: "subject-one", ClientID: "client-one",
			Scopes: []string{"openid", "profile"}, ExpiresAt: now.Add(2 * time.Hour),
			Name: "Test User", Email: "test@example.invalid",
		},
	}
}

func TestRefreshLeaseJSONExcludesBearerMaterial(t *testing.T) {
	lease := session.RefreshLease{SessionID: "session-secret-one", Version: 1, Token: "lease-secret-one", ExpiresAt: time.Now()}
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{lease.SessionID, lease.Token} {
		if strings.Contains(string(data), secret) {
			t.Fatal("refresh lease JSON leaked bearer material")
		}
	}
}

func TestRefreshLeaseCommitUsesOwnerAndVersionCAS(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-refresh-secret")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if lease.SessionID != record.ID || lease.Version != 1 || lease.Token == "" || !lease.ExpiresAt.Equal(f.clock.Now().Add(30*time.Second)) {
		t.Fatal("refresh lease fields do not bind the session, version, owner, and deadline")
	}
	if _, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("second lease error = %v, want conflict", err)
	}
	leaseKey := hashedKey(f.prefix, "refresh", record.ID)
	leaseValue, err := f.client.Get(ctx, leaseKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(leaseKey, record.ID) || strings.Contains(leaseValue, lease.Token) || strings.Contains(leaseValue, record.ID) {
		t.Fatal("refresh lease storage leaked session or owner bearer material")
	}
	wrong := lease
	wrong.Token = "wrong-lease-owner"
	if err := f.store.ReleaseRefresh(ctx, wrong); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("wrong owner release error = %v, want conflict", err)
	}
	if _, err := f.store.CommitRefresh(ctx, wrong, refreshedTokens(f.clock.Now())); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("wrong owner commit error = %v, want conflict", err)
	}

	updated, err := f.store.CommitRefresh(ctx, lease, refreshedTokens(f.clock.Now()))
	if err != nil {
		t.Fatal(err)
	}
	record.Version = 2
	record.Tokens = refreshedTokens(f.clock.Now())
	if !reflect.DeepEqual(updated, record) {
		t.Fatal("refresh commit changed immutable session fields or lost refreshed tokens")
	}
	read, err := f.store.ReadSession(ctx, record.ID)
	if err != nil || !reflect.DeepEqual(read, record) {
		t.Fatal("refreshed session was not persisted at the next version")
	}
	if err := f.store.ReleaseRefresh(ctx, lease); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("committed lease remained reusable: %v", err)
	}
	if _, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("stale session version acquired a lease: %v", err)
	}
	key := hashedKey(f.prefix, "session", record.ID)
	row, err := f.client.HGetAll(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{record.Tokens.AccessToken, record.Tokens.RefreshToken, record.Tokens.IDToken, lease.Token} {
		for name, value := range row {
			if strings.Contains(name, secret) || strings.Contains(value, secret) {
				t.Fatal("refresh commit leaked token or lease material to Redis")
			}
		}
	}
}

func TestRefreshCommitRequiresLiveRedisLeaseEvenIfCallerExtendsDeadline(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-expired-refresh")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	leaseKey := hashedKey(f.prefix, "refresh", record.ID)
	if err := f.client.Persist(ctx, leaseKey).Err(); err != nil {
		t.Fatal(err)
	}
	lease.ExpiresAt = lease.ExpiresAt.Add(time.Hour)
	if _, err := f.store.CommitRefresh(ctx, lease, refreshedTokens(f.clock.Now())); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("commit with non-expiring Redis lease error = %v, want not found", err)
	}
	if err := f.store.ReleaseRefresh(ctx, lease); err != nil {
		t.Fatalf("owner could not clean malformed refresh lease: %v", err)
	}
	read, err := f.store.ReadSession(ctx, record.ID)
	if err != nil || read.Version != 1 || read.Tokens.AccessToken != record.Tokens.AccessToken {
		t.Fatal("rejected refresh changed the session")
	}
}

func TestDeleteSessionPreventsLateRefreshFromResurrectingIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-delete-secret")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteSession(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CommitRefresh(ctx, lease, refreshedTokens(f.clock.Now())); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("late refresh commit error = %v, want not found", err)
	}
	if _, err := f.store.ReadSession(ctx, record.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted session was resurrected: %v", err)
	}
	if err := f.store.DeleteSession(ctx, record.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("second delete error = %v, want not found", err)
	}
}

func TestConcurrentLogoutAndRefreshNeverLeaveASession(t *testing.T) {
	for iteration := 0; iteration < 24; iteration++ {
		f := newFixture(t)
		ctx := context.Background()
		record := recordAt(f.clock.Now(), "session-logout-race")
		if err := f.store.CreateSession(ctx, record); err != nil {
			t.Fatal(err)
		}
		lease, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		commitResult := make(chan error, 1)
		deleteResult := make(chan error, 1)
		go func() {
			<-start
			_, err := f.store.CommitRefresh(ctx, lease, refreshedTokens(f.clock.Now()))
			commitResult <- err
		}()
		go func() {
			<-start
			deleteResult <- f.store.DeleteSession(ctx, record.ID)
		}()
		close(start)
		commitErr, deleteErr := <-commitResult, <-deleteResult
		if commitErr != nil && !errors.Is(commitErr, session.ErrNotFound) {
			t.Fatalf("iteration %d commit error = %v", iteration, commitErr)
		}
		if deleteErr != nil {
			t.Fatalf("iteration %d delete error = %v", iteration, deleteErr)
		}
		if _, err := f.store.ReadSession(ctx, record.ID); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("iteration %d left a session after logout/refresh race: %v", iteration, err)
		}
	}
}

func TestDeleteSessionRemovesAnOrphanedRefreshLease(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-orphan-refresh")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Del(ctx, hashedKey(f.prefix, "session", record.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteSession(ctx, record.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("delete missing session error = %v, want not found", err)
	}
	if keys := prefixKeys(t, f.client, f.prefix); len(keys) != 0 {
		t.Fatal("delete left an orphaned refresh lease")
	}
}

func TestRefreshLeaseReleaseAndConcurrentAcquisition(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-concurrent-refresh")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	const callers = 16
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	results := make(chan error, callers)
	leases := make(chan session.RefreshLease, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			lease, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second)
			if err == nil {
				leases <- lease
			}
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	winners := 0
	var winner session.RefreshLease
	for range callers {
		err := <-results
		if err == nil {
			winners++
			winner = <-leases
		} else if !errors.Is(err, session.ErrConflict) {
			t.Errorf("losing acquire error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("refresh lease winners = %d, want 1", winners)
	}
	if err := f.store.ReleaseRefresh(ctx, winner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AcquireRefresh(ctx, record.ID, 1, 30*time.Second); err != nil {
		t.Fatal("own release did not make the lease available")
	}
}

func TestRefreshRejectsInvalidLeaseInputsWithoutWritingKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	record := recordAt(f.clock.Now(), "session-invalid-refresh")
	if err := f.store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		id      string
		version int64
		ttl     time.Duration
	}{
		{"", 1, time.Second}, {record.ID, 0, time.Second}, {record.ID, 1, 0},
		{record.ID, 1, 30*time.Second + time.Millisecond},
	} {
		if _, err := f.store.AcquireRefresh(ctx, input.id, input.version, input.ttl); !errors.Is(err, session.ErrInvalid) {
			t.Fatalf("invalid acquire error = %v, want invalid", err)
		}
	}
	if len(prefixKeys(t, f.client, f.prefix)) != 1 {
		t.Fatal("invalid refresh acquire wrote a lease key")
	}
}
