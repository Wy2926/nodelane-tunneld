package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func TestControlIntegrationDeleteVersusAccountStart(t *testing.T) {
	f := newControlJoinFixture(t)
	cmd := f.startCommand("start-before-delete")
	var started domain.StartResult
	errs := controlJoinCalls(func() (err error) {
		started, err = f.store.StartAccountRun(f.ctx, cmd)
		return err
	}, func() error {
		_, err := f.store.DeleteRoute(f.ctx, f.account.ID, f.route.ID)
		return err
	})
	if errs[1] != nil {
		t.Fatalf("delete: %v", errs[1])
	}
	if errs[0] != nil && !errors.Is(errs[0], domain.ErrRouteDeleted) {
		t.Fatalf("start: %v", errs[0])
	}
	wantRuns := 0
	if errs[0] == nil {
		wantRuns = 1
		requireControlJoinDenied(t, f, started)
	} else if started != (domain.StartResult{}) {
		t.Fatal("failed start returned a partial result")
	}
	f.requireCount(t, "tunnel_runs", wantRuns)
	f.requireCount(t, "run_credentials", wantRuns)
	f.requireCount(t, "operation_replays", 1+wantRuns)
	f.requireDeleted(t)
	if replay, err := f.store.StartAccountRun(f.ctx, cmd); err == nil || replay != (domain.StartResult{}) {
		t.Fatal("start replay returned a usable or partial result after deletion")
	}
}

func TestControlIntegrationDeleteVersusLaunchRedemption(t *testing.T) {
	f := newControlJoinFixture(t)
	issued, err := f.store.IssueLaunchCode(f.ctx, domain.IssueLaunchCodeCommand{AccountID: f.account.ID, RouteID: f.route.ID})
	if err != nil {
		t.Fatal(err)
	}
	cmd := domain.LaunchRedeemCommand{Token: issued.Token, Nonce: "redeem-before-delete", RequestIP: netip.MustParseAddr("203.0.113.20")}
	var started domain.StartResult
	errs := controlJoinCalls(func() (err error) {
		started, err = f.store.RedeemLaunchCode(f.ctx, cmd)
		return err
	}, func() error {
		_, err := f.store.DeleteRoute(f.ctx, f.account.ID, f.route.ID)
		return err
	})
	if errs[1] != nil {
		t.Fatalf("delete: %v", errs[1])
	}
	if errs[0] != nil && !errors.Is(errs[0], domain.ErrRouteDeleted) && !errors.Is(errs[0], domain.ErrLaunchCodeRevoked) {
		t.Fatalf("redeem: %v", errs[0])
	}
	var redeemed, revoked bool
	if err := f.db.QueryRowContext(f.ctx, `SELECT redeemed_at IS NOT NULL, revoked_at IS NOT NULL FROM route_launch_codes WHERE id=$1`, issued.Code.ID).Scan(&redeemed, &revoked); err != nil {
		t.Fatal(err)
	}
	wantRuns := 0
	if errs[0] == nil {
		wantRuns = 1
		if !redeemed {
			t.Fatal("successful redemption did not consume its code")
		}
		requireControlJoinDenied(t, f, started)
	} else {
		if started != (domain.StartResult{}) || redeemed || !revoked {
			t.Fatal("failed redemption leaked a result, consumed a code, or bypassed delete revocation")
		}
	}
	f.requireCount(t, "tunnel_runs", wantRuns)
	f.requireCount(t, "run_credentials", wantRuns)
	f.requireCount(t, "operation_replays", 1+wantRuns)
	f.requireDeleted(t)
	if replay, err := f.store.RedeemLaunchCode(f.ctx, cmd); err == nil || replay != (domain.StartResult{}) {
		t.Fatal("launch replay returned a usable or partial result after deletion")
	}
}

func TestControlIntegrationRestoreDoesNotReviveStoppedRun(t *testing.T) {
	f := newControlJoinFixture(t)
	issued, err := f.store.IssueLaunchCode(f.ctx, domain.IssueLaunchCodeCommand{AccountID: f.account.ID, RouteID: f.route.ID})
	if err != nil {
		t.Fatal(err)
	}
	cmd := f.startCommand("original-start")
	original, err := f.store.StartAccountRun(f.ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DeleteRoute(f.ctx, f.account.ID, f.route.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := f.store.RestoreRoute(f.ctx, f.account.ID, f.route.ID)
	if err != nil || restored.ID != f.route.ID || restored.ProxyName != f.route.ProxyName || restored.Status != domain.RouteActive {
		t.Fatalf("restore did not preserve the active route identity: %v", err)
	}
	requireControlJoinDenied(t, f, original)
	if replay, err := f.store.StartAccountRun(f.ctx, cmd); err == nil || replay != (domain.StartResult{}) {
		t.Fatal("restore revived the original startup replay")
	}
	if result, err := f.store.StartAccountRun(f.ctx, f.startCommand("new-start")); !errors.Is(err, domain.ErrRunAlreadyActive) || result != (domain.StartResult{}) {
		t.Fatalf("stopping slot was released without offline evidence: %v", err)
	}
	if _, err := f.store.ConfirmOffline(f.ctx, domain.RunDisconnectEvidence{
		RunID: original.Run.ID, RouteID: f.route.ID, ProxyName: f.route.ProxyName, ObservedOffline: true, CurrentConnections: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := f.store.RedeemLaunchCode(f.ctx, domain.LaunchRedeemCommand{Token: issued.Token, Nonce: "revoked-launch", RequestIP: cmd.RequestIP}); !errors.Is(err, domain.ErrLaunchCodeRevoked) || result != (domain.StartResult{}) {
		t.Fatalf("restore revived a revoked launch code after the slot was released: %v", err)
	}
	replacement, err := f.store.StartAccountRun(f.ctx, f.startCommand("new-start"))
	if err != nil || replacement.Run.ID == original.Run.ID {
		t.Fatalf("new run after confirmed offline: %v", err)
	}
	if _, err := f.store.ConfirmOnline(f.ctx, domain.RunRegistrationEvidence{
		RunID: original.Run.ID, RouteID: f.route.ID, ProxyName: f.route.ProxyName, ObservedOnline: true, ConnectedIP: cmd.RequestIP,
	}); err == nil {
		t.Fatal("late registration revived the stopped run")
	}
	if _, err := f.store.AuthorizeRun(f.ctx, domain.RunProof{RunID: replacement.Run.ID, Token: replacement.CredentialToken}); err != nil {
		t.Fatalf("late evidence affected the replacement run: %v", err)
	}
	f.requireCount(t, "tunnel_routes", 1)
	f.requireCount(t, "tunnel_runs", 2)
}

func TestControlIntegrationRestoreVersusStart(t *testing.T) {
	f := newControlJoinFixture(t)
	if _, err := f.store.DeleteRoute(f.ctx, f.account.ID, f.route.ID); err != nil {
		t.Fatal(err)
	}
	cmd := f.startCommand("start-during-restore")
	var started domain.StartResult
	errs := controlJoinCalls(func() (err error) {
		started, err = f.store.StartAccountRun(f.ctx, cmd)
		return err
	}, func() error {
		_, err := f.store.RestoreRoute(f.ctx, f.account.ID, f.route.ID)
		return err
	})
	if errs[1] != nil {
		t.Fatalf("restore: %v", errs[1])
	}
	if errs[0] != nil && !errors.Is(errs[0], domain.ErrRouteDeleted) {
		t.Fatalf("start during restore: %v", errs[0])
	}
	if errs[0] != nil && started != (domain.StartResult{}) {
		t.Fatal("start preceding restore leaked a partial result")
	}
	after, err := f.store.StartAccountRun(f.ctx, cmd)
	if err != nil {
		t.Fatalf("retry after completed restore: %v", err)
	}
	if errs[0] == nil {
		if !after.Replayed || after.Run.ID != started.Run.ID || after.CredentialToken != started.CredentialToken {
			t.Fatal("retry after restore allocated a second successful start")
		}
	} else if after.Replayed {
		t.Fatal("failed pre-restore start left a committed replay")
	}
	if _, err := f.store.AuthorizeRun(f.ctx, domain.RunProof{RunID: after.Run.ID, Token: after.CredentialToken}); err != nil {
		t.Fatalf("restored route start is not usable: %v", err)
	}
	f.requireCount(t, "tunnel_routes", 1)
	f.requireCount(t, "tunnel_runs", 1)
	f.requireCount(t, "run_credentials", 1)
	f.requireCount(t, "operation_replays", 2)
}

func TestControlIntegrationStopVersusReplay(t *testing.T) {
	for _, viaLaunch := range []bool{false, true} {
		name := "account_start"
		if viaLaunch {
			name = "launch_redemption"
		}
		t.Run(name, func(t *testing.T) {
			f := newControlJoinFixture(t)
			cmd := f.startCommand("retry-start")
			start := func() (domain.StartResult, error) { return f.store.StartAccountRun(f.ctx, cmd) }
			if viaLaunch {
				issued, err := f.store.IssueLaunchCode(f.ctx, domain.IssueLaunchCodeCommand{AccountID: f.account.ID, RouteID: f.route.ID})
				if err != nil {
					t.Fatal(err)
				}
				redeem := domain.LaunchRedeemCommand{Token: issued.Token, Nonce: "retry-redeem", RequestIP: cmd.RequestIP}
				start = func() (domain.StartResult, error) { return f.store.RedeemLaunchCode(f.ctx, redeem) }
			}
			original, err := start()
			if err != nil {
				t.Fatal(err)
			}
			var replay domain.StartResult
			errs := controlJoinCalls(func() (err error) {
				replay, err = start()
				return err
			}, func() error {
				_, err := f.store.RequestOwnedStop(f.ctx, f.account.ID, f.route.ID)
				return err
			})
			if errs[1] != nil {
				t.Fatalf("stop: %v", errs[1])
			}
			if errs[0] == nil {
				if !replay.Replayed || replay.Run.ID != original.Run.ID || replay.CredentialToken != original.CredentialToken {
					t.Fatal("replay preceding stop allocated a different result")
				}
			} else if !errors.Is(errs[0], domain.ErrRunStopped) || replay != (domain.StartResult{}) {
				t.Fatalf("replay following stop did not fail closed: %v", errs[0])
			}
			requireControlJoinDenied(t, f, original)
			if result, err := start(); err == nil || result != (domain.StartResult{}) {
				t.Fatal("completed stop was bypassed by a later replay")
			}
			var status, desired string
			if err := f.db.QueryRowContext(f.ctx, `SELECT status, desired_state FROM tunnel_runs WHERE id=$1`, original.Run.ID).Scan(&status, &desired); err != nil {
				t.Fatal(err)
			}
			if status != "stopping" || desired != "stopped" {
				t.Fatalf("stop state = %s/%s", status, desired)
			}
			f.requireCount(t, "tunnel_runs", 1)
			f.requireCount(t, "run_credentials", 1)
			f.requireCount(t, "operation_replays", 2)
		})
	}
}

func TestControlIntegrationNameTransferRejectsStaleAuthorization(t *testing.T) {
	f := newControlJoinFixture(t)
	issued, err := f.store.IssueLaunchCode(f.ctx, domain.IssueLaunchCodeCommand{AccountID: f.account.ID, RouteID: f.route.ID})
	if err != nil {
		t.Fatal(err)
	}
	original, err := f.store.StartAccountRun(f.ctx, f.startCommand("old-owner-start"))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := f.store.DeleteRoute(f.ctx, f.account.ID, f.route.ID)
	if err != nil || deleted.RecoverableUntil == nil {
		t.Fatalf("delete did not set recovery cutoff: %v", err)
	}
	f.clock.Set(f.clock.Now().Add(7 * 24 * time.Hour))
	if !f.clock.Now().Equal(*deleted.RecoverableUntil) {
		t.Fatal("recovery cutoff differs from exactly seven days")
	}
	if released, err := f.store.ReleaseNeverGranted(f.ctx, original.Run.ID); err != nil || released.Status != domain.RunOffline {
		t.Fatalf("expired never-granted run was not drained: %+v %v", released, err)
	}
	other, err := f.store.ResolveAccount(f.ctx, "https://join.issuer.test", "other-owner")
	if err != nil {
		t.Fatal(err)
	}
	transferred, err := f.store.CreateRoute(f.ctx, domain.CreateRouteCommand{
		AccountID: other.ID, Protocol: "http", Subdomain: f.route.Subdomain, IdempotencyKey: "transfer-label",
	})
	if err != nil || transferred.Route.ID == f.route.ID || transferred.Route.ProxyName != transferred.Route.ID {
		t.Fatalf("name transfer did not get a new route/proxy identity: %v", err)
	}
	if _, err := f.store.RestoreRoute(f.ctx, f.account.ID, f.route.ID); err == nil {
		t.Fatal("old route remained recoverable after the exact cutoff")
	}
	if _, err := f.store.GetRoute(f.ctx, f.account.ID, transferred.Route.ID); !errors.Is(err, domain.ErrRouteNotFound) {
		t.Fatalf("old owner could distinguish/access the transferred route: %v", err)
	}
	requireControlJoinDenied(t, f, original)
	if replay, err := f.store.StartAccountRun(f.ctx, f.startCommand("old-owner-start")); err == nil || replay != (domain.StartResult{}) {
		t.Fatal("old account replay survived name transfer")
	}
	if result, err := f.store.RedeemLaunchCode(f.ctx, domain.LaunchRedeemCommand{Token: issued.Token, Nonce: "old-launch", RequestIP: netip.MustParseAddr("203.0.113.20")}); err == nil || result != (domain.StartResult{}) {
		t.Fatal("old launch code survived name transfer")
	}
	newRun, err := f.store.StartAccountRun(f.ctx, domain.AccountStartCommand{
		AccountID: other.ID, RouteID: transferred.Route.ID, IdempotencyKey: "new-owner-start", RequestIP: netip.MustParseAddr("203.0.113.21"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ConfirmOffline(f.ctx, domain.RunDisconnectEvidence{
		RunID: original.Run.ID, RouteID: transferred.Route.ID, ProxyName: transferred.Route.ProxyName, ObservedOffline: true, CurrentConnections: 0,
	}); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
		t.Fatalf("stale evidence was allowed to target the new route: %v", err)
	}
	if _, err := f.store.AuthorizeRun(f.ctx, domain.RunProof{RunID: newRun.Run.ID, Token: newRun.CredentialToken}); err != nil {
		t.Fatalf("stale owner/evidence affected the new run: %v", err)
	}
	f.requireCount(t, "tunnel_routes", 2)
	f.requireCount(t, "tunnel_runs", 2)
}

type controlJoinFixture struct {
	ctx     context.Context
	store   *ControlPostgres
	db      *sql.DB
	clock   *controlTestClock
	account domain.Account
	route   domain.Route
}

func newControlJoinFixture(t *testing.T) controlJoinFixture {
	t.Helper()
	fixture := newControlTestFixture(t)
	clock := &controlTestClock{value: time.Date(2026, 9, 5, 4, 34, 56, 123456000, time.UTC)}
	fixture.Options.Now = clock.Now
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	p, err := OpenControlPostgres(ctx, fixture.DSN, fixture.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	account, err := p.ResolveAccount(ctx, "https://join.issuer.test", "original-owner")
	if err != nil {
		t.Fatal(err)
	}
	created, err := p.CreateRoute(ctx, domain.CreateRouteCommand{
		AccountID: account.ID, Protocol: "http", Subdomain: "join-route", IdempotencyKey: "create-route",
	})
	if err != nil {
		t.Fatal(err)
	}
	return controlJoinFixture{ctx: ctx, store: p, db: fixture.DB, clock: clock, account: account, route: created.Route}
}

func (f controlJoinFixture) startCommand(key string) domain.AccountStartCommand {
	return domain.AccountStartCommand{AccountID: f.account.ID, RouteID: f.route.ID, IdempotencyKey: key, RequestIP: netip.MustParseAddr("203.0.113.20")}
}

func (f controlJoinFixture) requireCount(t *testing.T, table string, want int) {
	t.Helper()
	if got := controlTestTableCount(t, f.db, table); got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func (f controlJoinFixture) requireDeleted(t *testing.T) {
	t.Helper()
	route, err := f.store.GetRoute(f.ctx, f.account.ID, f.route.ID)
	if err != nil || route.Status != domain.RouteDeleted {
		t.Fatalf("route did not remain deleted: %v", err)
	}
}

func requireControlJoinDenied(t *testing.T, f controlJoinFixture, result domain.StartResult) {
	t.Helper()
	if _, err := f.store.AuthorizeRun(f.ctx, domain.RunProof{RunID: result.Run.ID, Token: result.CredentialToken}); err == nil {
		t.Fatal("stopped/deleted run remained authorized")
	}
}

func controlJoinCalls(first, second func() error) [2]error {
	start := make(chan struct{})
	var wg sync.WaitGroup
	var errs [2]error
	for index, call := range []func() error{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[index] = call()
		}()
	}
	close(start)
	wg.Wait()
	return errs
}
