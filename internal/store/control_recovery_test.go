package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func TestControlNewRunRecordsExplicitNeverGrantedEvidence(t *testing.T) {
	h := newControlRunHarness(t)
	started := h.start(t, "new-run-registration-evidence")
	if started.Run.ProxyRegistrationGranted {
		t.Fatalf("new run evidence=%v, want explicit false", started.Run.ProxyRegistrationGranted)
	}
	if _, err := h.api.AuthorizeRun(context.Background(), controlProof(started)); err != nil {
		t.Fatal(err)
	}
	var stored bool
	if err := h.fixture.DB.QueryRow(`SELECT proxy_registration_granted FROM tunnel_runs WHERE id=$1`, started.Run.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored {
		t.Fatal("ordinary authorization changed registration evidence")
	}
}

func TestControlRunReplayPreservesExplicitNeverGrantedEvidence(t *testing.T) {
	h := newControlRunHarness(t)
	const idempotencyKey = "replayed-registration-evidence"
	started := h.start(t, idempotencyKey)

	replayed, err := h.api.StartAccountRun(context.Background(), h.startCommand(idempotencyKey))
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Run.ProxyRegistrationGranted {
		t.Fatalf("replayed run evidence=%v replayed=%t, want explicit false", replayed.Run.ProxyRegistrationGranted, replayed.Replayed)
	}
	if !reflect.DeepEqual(replayed.Run, started.Run) {
		t.Fatalf("replayed run changed original response: got=%+v want=%+v", replayed.Run, started.Run)
	}
}

func TestControlAuthorizeProxyRegistrationRecordsGrantWithoutRenewingLease(t *testing.T) {
	h := newControlRunHarness(t)
	started := h.start(t, "proxy-registration-grant")
	authorized, err := h.store.AuthorizeProxyRegistration(context.Background(), controlProof(started))
	if err != nil {
		t.Fatal(err)
	}
	if !authorized.Run.ProxyRegistrationGranted || authorized.Run.LeaseExpiresAt != nil {
		t.Fatalf("starting registration authorization=%+v", authorized.Run)
	}

	online := h.online(t, started)
	lease := *online.LeaseExpiresAt
	h.clock.Set(h.clock.Now().Add(30 * time.Second))
	authorized, err = h.store.AuthorizeProxyRegistration(context.Background(), controlProof(started))
	if err != nil {
		t.Fatal(err)
	}
	if authorized.Run.LeaseExpiresAt == nil || !authorized.Run.LeaseExpiresAt.Equal(lease) {
		t.Fatalf("registration authorization renewed lease: got=%v want=%v", authorized.Run.LeaseExpiresAt, lease)
	}
}

func TestControlReleaseNeverGrantedRequiresStoppedOrExpiredExplicitFalse(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		h := newControlRunHarness(t)
		started := h.start(t, "release-never-granted-stopped")
		if _, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
			t.Fatalf("running run released: %v", err)
		}
		if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(started)); err != nil {
			t.Fatal(err)
		}
		released, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if released.Status != domain.RunOffline || released.StoppedAt == nil || released.ProxyRegistrationGranted {
			t.Fatalf("released run=%+v", released)
		}
		if repeated, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID); err != nil || repeated.Status != domain.RunOffline {
			t.Fatalf("idempotent release=%+v err=%v", repeated, err)
		}
		var revokedAt *time.Time
		if err := h.fixture.DB.QueryRow(`SELECT revoked_at FROM run_credentials WHERE run_id=$1`, started.Run.ID).Scan(&revokedAt); err != nil || revokedAt == nil {
			t.Fatalf("credential was not revoked: %v %v", revokedAt, err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		h := newControlRunHarness(t)
		started := h.start(t, "release-never-granted-expired")
		h.clock.Set(started.Run.ConnectDeadlineAt)
		released, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID)
		if err != nil || released.Status != domain.RunOffline || released.StopReason != "connect_timeout" {
			t.Fatalf("expired release=%+v err=%v", released, err)
		}
	})

	h := newControlRunHarness(t)
	started := h.start(t, "release-refuses-granted")
	if _, err := h.store.AuthorizeProxyRegistration(context.Background(), controlProof(started)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(started)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
		t.Fatalf("granted evidence released: %v", err)
	}
	var status domain.RunStatus
	var revokedAt *time.Time
	if err := h.fixture.DB.QueryRow(`SELECT r.status,c.revoked_at FROM tunnel_runs r JOIN run_credentials c ON c.run_id=r.id WHERE r.id=$1`, started.Run.ID).Scan(&status, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if status != domain.RunStopping || revokedAt != nil {
		t.Fatalf("refused release mutated authority: status=%s revoked=%v", status, revokedAt)
	}
}

func TestControlProxyGrantAndNeverGrantedReleaseCannotBothWin(t *testing.T) {
	h := newControlRunHarness(t)
	started := h.start(t, "grant-release-race")
	gate := make(chan struct{})
	grantDone := make(chan error, 1)
	releaseDone := make(chan error, 1)
	var ready sync.WaitGroup
	ready.Add(2)
	go func() {
		ready.Done()
		<-gate
		_, err := h.store.AuthorizeProxyRegistration(context.Background(), controlProof(started))
		grantDone <- err
	}()
	go func() {
		ready.Done()
		<-gate
		if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(started)); err != nil {
			releaseDone <- err
			return
		}
		_, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID)
		releaseDone <- err
	}()
	ready.Wait()
	close(gate)
	grantErr, releaseErr := <-grantDone, <-releaseDone
	grantWon, releaseWon := grantErr == nil, releaseErr == nil
	if grantWon == releaseWon {
		t.Fatalf("grant/release winners: grant=%v release=%v", grantErr, releaseErr)
	}
	if grantWon && !errors.Is(releaseErr, domain.ErrRunEvidenceInvalid) {
		t.Fatalf("grant winner produced release error=%v", releaseErr)
	}
	if releaseWon && !errors.Is(grantErr, domain.ErrRunStopped) {
		t.Fatalf("release winner produced grant error=%v", grantErr)
	}
}

func TestControlConfirmOfflineAcceptsOnlyAuthorizedDrainedAbsentProxyEvidence(t *testing.T) {
	t.Run("authorized and drained", func(t *testing.T) {
		h := newControlRunHarness(t)
		started := h.start(t, "drained-absent-authorized")
		if _, err := h.store.AuthorizeProxyRegistration(context.Background(), controlProof(started)); err != nil {
			t.Fatal(err)
		}
		if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(started)); err != nil {
			t.Fatal(err)
		}
		got, err := h.api.ConfirmOffline(context.Background(), domain.RunDisconnectEvidence{
			RunID: started.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ProxyName,
			ProxyNotObserved: true, ConfirmedClientDisconnected: true,
		})
		if err != nil || got.Status != domain.RunOffline {
			t.Fatalf("drained absent proxy=%+v err=%v", got, err)
		}
	})

	t.Run("never granted", func(t *testing.T) {
		h := newControlRunHarness(t)
		started := h.start(t, "drained-absent-never-granted")
		if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(started)); err != nil {
			t.Fatal(err)
		}
		if _, err := h.api.ConfirmOffline(context.Background(), domain.RunDisconnectEvidence{
			RunID: started.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ProxyName,
			ProxyNotObserved: true, ConfirmedClientDisconnected: true,
		}); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
			t.Fatalf("never-granted absent evidence accepted: %v", err)
		}
	})

	t.Run("invalid combinations", func(t *testing.T) {
		h := newControlRunHarness(t)
		started := h.start(t, "drained-absent-invalid-shapes")
		if _, err := h.store.AuthorizeProxyRegistration(context.Background(), controlProof(started)); err != nil {
			t.Fatal(err)
		}
		if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(started)); err != nil {
			t.Fatal(err)
		}
		for name, mutate := range map[string]func(*domain.RunDisconnectEvidence){
			"missing disconnected client": func(e *domain.RunDisconnectEvidence) { e.ConfirmedClientDisconnected = false },
			"missing absent proxy":        func(e *domain.RunDisconnectEvidence) { e.ProxyNotObserved = false },
			"mixed offline sample":        func(e *domain.RunDisconnectEvidence) { e.ObservedOffline = true },
			"active connection":           func(e *domain.RunDisconnectEvidence) { e.CurrentConnections = 1 },
		} {
			t.Run(name, func(t *testing.T) {
				evidence := domain.RunDisconnectEvidence{
					RunID: started.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ProxyName,
					ProxyNotObserved: true, ConfirmedClientDisconnected: true,
				}
				mutate(&evidence)
				if _, err := h.api.ConfirmOffline(context.Background(), evidence); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
					t.Fatalf("invalid evidence accepted: %+v err=%v", evidence, err)
				}
			})
		}
	})
}

func TestControlPendingRunReconciliationIsBoundedFairAndAuthorityReadOnly(t *testing.T) {
	h := newControlRunHarness(t)
	stopping := h.start(t, "pending-stopping")
	if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(stopping)); err != nil {
		t.Fatal(err)
	}

	secondRoute := createControlTestRoute(t, h.store, h.account.ID, "pending-expired")
	expired, err := h.store.StartAccountRun(context.Background(), domain.AccountStartCommand{
		AccountID: h.account.ID, RouteID: secondRoute.ID, IdempotencyKey: "pending-expired-run", RequestIP: netip.MustParseAddr("192.0.2.16"),
	})
	if err != nil {
		t.Fatal(err)
	}
	thirdRoute := createControlTestRoute(t, h.store, h.account.ID, "pending-live")
	live, err := h.store.StartAccountRun(context.Background(), domain.AccountStartCommand{
		AccountID: h.account.ID, RouteID: thirdRoute.ID, IdempotencyKey: "pending-live-run", RequestIP: netip.MustParseAddr("192.0.2.17"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.fixture.DB.Exec(`UPDATE tunnel_runs SET connect_deadline_at=$2 WHERE id=$1`, live.Run.ID, expired.Run.ConnectDeadlineAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(expired.Run.ConnectDeadlineAt)

	if got, err := h.store.PendingRunReconciliation(context.Background(), 0); !errors.Is(err, domain.ErrInvalidRequest) || got != nil {
		t.Fatalf("invalid limit=%+v err=%v", got, err)
	}
	first, err := h.store.PendingRunReconciliation(context.Background(), 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first fair batch=%+v err=%v", first, err)
	}
	second, err := h.store.PendingRunReconciliation(context.Background(), 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("second fair batch=%+v err=%v", second, err)
	}
	if first[0].Run.ID == second[0].Run.ID {
		t.Fatalf("held head starved later work: first=%s second=%s", first[0].Run.ID, second[0].Run.ID)
	}

	items, err := h.store.PendingRunReconciliation(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("pending count=%d items=%+v", len(items), items)
	}
	got := map[string]domain.RunAuthorization{}
	for _, item := range items {
		got[item.Run.ID] = item
		if item.Route.ID != item.Run.RouteID || item.CredentialID == "" {
			t.Fatalf("incomplete reconciliation item=%+v", item)
		}
	}
	if _, ok := got[stopping.Run.ID]; !ok {
		t.Fatalf("stopping run missing: %+v", items)
	}
	if _, ok := got[expired.Run.ID]; !ok {
		t.Fatalf("expired run missing: %+v", items)
	}
	if _, ok := got[live.Run.ID]; ok {
		t.Fatalf("live run selected: %+v", items)
	}
	var expiredStatus domain.RunStatus
	if err := h.fixture.DB.QueryRow(`SELECT status FROM tunnel_runs WHERE id=$1`, expired.Run.ID).Scan(&expiredStatus); err != nil {
		t.Fatal(err)
	}
	if expiredStatus != domain.RunStarting {
		t.Fatalf("discovery terminalized expired run: %s", expiredStatus)
	}
}

func TestControlExpiredNameReleaseWaitsForActiveRunDrainage(t *testing.T) {
	t.Run("scheduled", func(t *testing.T) {
		h := newControlRunHarness(t)
		started := h.start(t, "scheduled-name-release")
		deleted, err := h.store.DeleteRoute(context.Background(), h.account.ID, h.route.ID)
		if err != nil {
			t.Fatal(err)
		}
		h.clock.Set(*deleted.RecoverableUntil)
		if count, err := h.store.ReleaseExpiredNames(context.Background(), 10); err != nil || count != 0 {
			t.Fatalf("undrained name release count=%d err=%v", count, err)
		}
		if _, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID); err != nil {
			t.Fatal(err)
		}
		if count, err := h.store.ReleaseExpiredNames(context.Background(), 10); err != nil || count != 1 {
			t.Fatalf("drained name release count=%d err=%v", count, err)
		}
	})

	t.Run("lazy create", func(t *testing.T) {
		h := newControlRunHarness(t)
		started := h.start(t, "lazy-name-release")
		deleted, err := h.store.DeleteRoute(context.Background(), h.account.ID, h.route.ID)
		if err != nil {
			t.Fatal(err)
		}
		h.clock.Set(*deleted.RecoverableUntil)
		other := resolveControlTestAccount(t, h.store, "lazy-name-other-account")
		cmd := domain.CreateRouteCommand{AccountID: other.ID, Protocol: "http", Subdomain: h.route.Subdomain, IdempotencyKey: "lazy-name-attempt-one"}
		if got, err := h.store.CreateRoute(context.Background(), cmd); !errors.Is(err, domain.ErrSubdomainConflict) || !reflect.DeepEqual(got, domain.CreateRouteResult{}) {
			t.Fatalf("undrained lazy reuse=%+v err=%v", got, err)
		}
		if _, err := h.store.ReleaseNeverGranted(context.Background(), started.Run.ID); err != nil {
			t.Fatal(err)
		}
		cmd.IdempotencyKey = "lazy-name-attempt-two"
		got, err := h.store.CreateRoute(context.Background(), cmd)
		if err != nil || got.Route.Subdomain != h.route.Subdomain {
			t.Fatalf("drained lazy reuse=%+v err=%v", got, err)
		}
	})
}

func TestControlRecoveryMethodsRejectMalformedIdentifiers(t *testing.T) {
	h := newControlRunHarness(t)
	if got, err := h.store.ReleaseNeverGranted(context.Background(), ""); !errors.Is(err, domain.ErrRunEvidenceInvalid) || !reflect.DeepEqual(got, domain.Run{}) {
		t.Fatalf("empty release=%+v err=%v", got, err)
	}
	if got, err := h.store.PendingRunReconciliation(context.Background(), 1001); !errors.Is(err, domain.ErrInvalidRequest) || got != nil {
		t.Fatalf("oversized reconciliation=%+v err=%v", got, err)
	}
	started := h.start(t, "invalid-registration-proof")
	bad := domain.RunProof{RunID: started.Run.ID, Token: fmt.Sprintf("%s.bad", started.CredentialID)}
	if got, err := h.store.AuthorizeProxyRegistration(context.Background(), bad); !errors.Is(err, domain.ErrInvalidRunProof) || !reflect.DeepEqual(got, domain.RunAuthorization{}) {
		t.Fatalf("invalid registration proof=%+v err=%v", got, err)
	}
}
