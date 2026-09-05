package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/routes"
)

func newControlRouteTestStore(t *testing.T) (*ControlPostgres, *sql.DB, *controlTestClock) {
	t.Helper()
	fixture := newControlTestFixture(t)
	clock := &controlTestClock{value: time.Date(2026, 9, 5, 4, 34, 56, 123456000, time.UTC)}
	fixture.Options.Now = clock.Now
	control, err := OpenControlPostgres(context.Background(), fixture.DSN, fixture.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	return control, fixture.DB, clock
}

func resolveControlTestAccount(t *testing.T, control *ControlPostgres, subject string) domain.Account {
	t.Helper()
	account, err := control.ResolveAccount(context.Background(), "https://issuer.test", subject)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func createControlTestRoute(t *testing.T, control *ControlPostgres, accountID, subdomain string) domain.Route {
	t.Helper()
	result, err := control.CreateRoute(context.Background(), domain.CreateRouteCommand{
		AccountID: accountID, Protocol: "http", Subdomain: subdomain, IdempotencyKey: "create-" + subdomain,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Route
}

func insertControlTestRun(t *testing.T, db *sql.DB, routeID, runID string, status domain.RunStatus, desired domain.DesiredState, now time.Time) {
	t.Helper()
	var connectedAt, connectedIP, leaseExpiresAt, stopRequestedAt any
	if status == domain.RunOnline {
		connectedAt = now
		connectedIP = "127.0.0.1"
		leaseExpiresAt = now.Add(90 * time.Second)
	}
	if desired == domain.DesiredStopped {
		stopRequestedAt = now
	}
	_, err := db.Exec(`INSERT INTO tunnel_runs
		(id, route_id, started_via, status, desired_state, request_ip, connected_ip, created_at,
		 connected_at, stop_requested_at, connect_deadline_at, lease_expires_at)
		VALUES ($1,$2,'device_login',$3,$4,'127.0.0.1',$5,$6,$7,$8,$9,$10)`,
		runID, routeID, status, desired, connectedIP, now, connectedAt, stopRequestedAt, now.Add(2*time.Minute), leaseExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
}

func controlTestBlockerPID(t *testing.T, tx *sql.Tx) int {
	t.Helper()
	var pid int
	if err := tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitForControlLockWait(t *testing.T, db *sql.DB, blockerPID int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := db.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid))
		)`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("production method did not reach the expected PostgreSQL lock wait")
}

func TestControlAccountProjectionIsConcurrentAndIssuerSubjectOnly(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	ctx := context.Background()
	const workers = 8
	accounts := make(chan domain.Account, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	start := make(chan struct{})
	for range workers {
		go func() {
			ready.Done()
			<-start
			account, err := control.ResolveAccount(ctx, "https://issuer.test", "stable-subject")
			accounts <- account
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	var accountID string
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		account := <-accounts
		if accountID == "" {
			accountID = account.ID
		}
		if account.ID != accountID || account.IdentityIssuer != "https://issuer.test" || account.IdentitySubject != "stable-subject" {
			t.Fatalf("unexpected projected account: %+v", account)
		}
	}
	if count := controlTestTableCount(t, db, "tunnel_accounts"); count != 1 {
		t.Fatalf("account rows = %d, want 1", count)
	}

	nextSeen := clock.Now().Add(time.Minute)
	clock.Set(nextSeen)
	again, err := control.ResolveAccount(ctx, "https://issuer.test", "stable-subject")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != accountID || !again.LastSeenAt.Equal(nextSeen) {
		t.Fatalf("repeat projection = %+v, want id %q and last seen %s", again, accountID, nextSeen)
	}
	otherIssuer, err := control.ResolveAccount(ctx, "https://other-issuer.test", "stable-subject")
	if err != nil {
		t.Fatal(err)
	}
	if otherIssuer.ID == accountID || controlTestTableCount(t, db, "tunnel_accounts") != 2 {
		t.Fatal("issuer and subject were not the complete stable identity key")
	}
}

func TestControlAccountSamplesLastSeenAfterWaitingForAccountLock(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	account := resolveControlTestAccount(t, control, "locked-account")
	blocker, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	blockerPID := controlTestBlockerPID(t, blocker)
	var locked string
	if err := blocker.QueryRow(`SELECT id::text FROM tunnel_accounts WHERE id=$1 FOR UPDATE`, account.ID).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	type resolveResult struct {
		account domain.Account
		err     error
	}
	result := make(chan resolveResult, 1)
	go func() {
		resolved, err := control.ResolveAccount(context.Background(), account.IdentityIssuer, account.IdentitySubject)
		result <- resolveResult{account: resolved, err: err}
	}()
	waitForControlLockWait(t, db, blockerPID)
	wantLastSeen := clock.Now().Add(time.Minute)
	clock.Set(wantLastSeen)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-result:
		if got.err != nil || !got.account.LastSeenAt.Equal(wantLastSeen) {
			t.Fatalf("resolved after lock wait = %+v, %v; want last seen %s", got.account, got.err, wantLastSeen)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResolveAccount did not finish after releasing account lock")
	}
}

func TestControlRouteQueriesEnforceOwnershipAndDeletedFilter(t *testing.T) {
	control, _, _ := newControlRouteTestStore(t)
	owner := resolveControlTestAccount(t, control, "owner")
	foreign := resolveControlTestAccount(t, control, "foreign")
	route := createControlTestRoute(t, control, owner.ID, "owned-route")

	got, err := control.GetRoute(context.Background(), owner.ID, route.ID)
	if err != nil || got.ID != route.ID || got.ProxyName != got.ID {
		t.Fatalf("owner get = %+v, %v", got, err)
	}
	if _, err := control.GetRoute(context.Background(), foreign.ID, route.ID); !errors.Is(err, domain.ErrRouteNotFound) {
		t.Fatalf("foreign get error = %v, want route not found", err)
	}
	if _, err := control.GetRoute(context.Background(), owner.ID, "rte_aaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, domain.ErrRouteNotFound) {
		t.Fatalf("missing get error = %v, want route not found", err)
	}
	routes, err := control.ListRoutes(context.Background(), owner.ID, domain.RouteQuery{})
	if err != nil || len(routes) != 1 || routes[0].ID != route.ID {
		t.Fatalf("active routes = %+v, %v", routes, err)
	}
	if routes, err := control.ListRoutes(context.Background(), foreign.ID, domain.RouteQuery{}); err != nil || len(routes) != 0 {
		t.Fatalf("foreign list = %+v, %v", routes, err)
	}
	if _, err := control.DeleteRoute(context.Background(), owner.ID, route.ID); err != nil {
		t.Fatal(err)
	}
	if routes, err := control.ListRoutes(context.Background(), owner.ID, domain.RouteQuery{}); err != nil || len(routes) != 0 {
		t.Fatalf("active routes after delete = %+v, %v", routes, err)
	}
	if routes, err := control.ListRoutes(context.Background(), owner.ID, domain.RouteQuery{Deleted: true}); err != nil || len(routes) != 1 || routes[0].ID != route.ID {
		t.Fatalf("deleted routes = %+v, %v", routes, err)
	}
}

func TestControlRouteCreateReplayIsAtomicAndBodyBound(t *testing.T) {
	control, db, _ := newControlRouteTestStore(t)
	policy, err := routes.NewStaticPolicyProvider(1)
	if err != nil {
		t.Fatal(err)
	}
	control.policy = policy
	account := resolveControlTestAccount(t, control, "replay-owner")
	cmd := domain.CreateRouteCommand{AccountID: account.ID, Protocol: "http", Subdomain: "replay-route", IdempotencyKey: "stable-key"}
	first, err := control.CreateRoute(context.Background(), cmd)
	if err != nil || first.Replayed {
		t.Fatalf("first create = %+v, %v", first, err)
	}
	second, err := control.CreateRoute(context.Background(), cmd)
	if err != nil || !second.Replayed || second.Route.ID != first.Route.ID {
		t.Fatalf("replay = %+v, %v; want route %q", second, err, first.Route.ID)
	}
	if controlTestTableCount(t, db, "tunnel_routes") != 1 || controlTestTableCount(t, db, "operation_replays") != 1 {
		t.Fatal("replay allocated a second route or replay row")
	}
	for _, changedLabel := range []string{"different-route", "Bad", "admin"} {
		changed := cmd
		changed.Subdomain = changedLabel
		if result, err := control.CreateRoute(context.Background(), changed); !errors.Is(err, domain.ErrIdempotencyConflict) || result.Route.ID != "" {
			t.Fatalf("changed replay label %q = %+v, %v", changedLabel, result, err)
		}
	}
	var ciphertext []byte
	if err := db.QueryRow(`SELECT response_ciphertext FROM operation_replays`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(first.Route.ID)) {
		t.Fatal("replay storage contains the plaintext route response")
	}

	if _, err := control.DeleteRoute(context.Background(), account.ID, first.Route.ID); err != nil {
		t.Fatal(err)
	}
	replayedDeleted, err := control.CreateRoute(context.Background(), cmd)
	if err != nil || !replayedDeleted.Replayed || replayedDeleted.Route.ID != first.Route.ID {
		t.Fatalf("deleted replay = %+v, %v", replayedDeleted, err)
	}
	stored, err := control.GetRoute(context.Background(), account.ID, first.Route.ID)
	if err != nil || stored.Status != domain.RouteDeleted || controlTestTableCount(t, db, "tunnel_routes") != 1 {
		t.Fatalf("deleted route was recreated: %+v, %v", stored, err)
	}
}

func TestControlRouteReplaySamplesExpiryAfterWaitingForReplayLock(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	account := resolveControlTestAccount(t, control, "locked-replay")
	cmd := domain.CreateRouteCommand{
		AccountID: account.ID, Protocol: "http", Subdomain: "locked-replay", IdempotencyKey: "locked-replay-key",
	}
	created, err := control.CreateRoute(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(created.Route.CreatedAt.Add(time.Minute))

	blocker, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	blockerPID := controlTestBlockerPID(t, blocker)
	var replayID string
	if err := blocker.QueryRow(`SELECT id FROM operation_replays WHERE route_id=$1 FOR UPDATE`, created.Route.ID).Scan(&replayID); err != nil {
		t.Fatal(err)
	}

	type createResult struct {
		result domain.CreateRouteResult
		err    error
	}
	result := make(chan createResult, 1)
	go func() {
		replayed, err := control.CreateRoute(context.Background(), cmd)
		result <- createResult{result: replayed, err: err}
	}()
	waitForControlLockWait(t, db, blockerPID)
	clock.Set(created.Route.CreatedAt.Add(2 * time.Minute))
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-result:
		if !errors.Is(got.err, identity.ErrReplayExpired) || got.result.Route.ID != "" {
			t.Fatalf("replay after lock-wait expiry = %+v, %v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CreateRoute replay did not finish after releasing replay lock")
	}
}

func TestControlRouteConcurrentCreateEnforcesFiveActiveLimit(t *testing.T) {
	control, db, _ := newControlRouteTestStore(t)
	account := resolveControlTestAccount(t, control, "quota-owner")
	const workers = 6
	results := make(chan error, workers)
	start := make(chan struct{})
	for i := range workers {
		go func(index int) {
			<-start
			_, err := control.CreateRoute(context.Background(), domain.CreateRouteCommand{
				AccountID: account.ID, Protocol: "http", Subdomain: fmt.Sprintf("quota-%d", index), IdempotencyKey: fmt.Sprintf("quota-key-%d", index),
			})
			results <- err
		}(i)
	}
	close(start)
	var created, limited int
	for range workers {
		switch err := <-results; {
		case err == nil:
			created++
		case errors.Is(err, domain.ErrRouteLimitReached):
			limited++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if created != 5 || limited != 1 || controlTestTableCount(t, db, "tunnel_routes") != 5 || controlTestTableCount(t, db, "operation_replays") != 5 {
		t.Fatalf("created=%d limited=%d route rows=%d replay rows=%d", created, limited, controlTestTableCount(t, db, "tunnel_routes"), controlTestTableCount(t, db, "operation_replays"))
	}
}

func TestControlRouteSameLabelRaceAcrossAccountsHasOneWinner(t *testing.T) {
	control, db, _ := newControlRouteTestStore(t)
	first := resolveControlTestAccount(t, control, "label-racer-1")
	second := resolveControlTestAccount(t, control, "label-racer-2")
	accounts := []domain.Account{first, second}
	results := make(chan error, 2)
	start := make(chan struct{})
	for index, account := range accounts {
		go func(index int, account domain.Account) {
			<-start
			_, err := control.CreateRoute(context.Background(), domain.CreateRouteCommand{
				AccountID: account.ID, Protocol: "http", Subdomain: "shared-label", IdempotencyKey: fmt.Sprintf("race-%d", index),
			})
			results <- err
		}(index, account)
	}
	close(start)
	var created, conflicted int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			created++
		case errors.Is(err, domain.ErrSubdomainConflict):
			conflicted++
		default:
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if created != 1 || conflicted != 1 || controlTestTableCount(t, db, "tunnel_routes") != 1 {
		t.Fatalf("created=%d conflicted=%d route rows=%d", created, conflicted, controlTestTableCount(t, db, "tunnel_routes"))
	}
}

func TestControlRouteDeleteStopsRunRevokesCodesAndFreesQuota(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	account := resolveControlTestAccount(t, control, "delete-owner")
	var routes []domain.Route
	for i := range 5 {
		routes = append(routes, createControlTestRoute(t, control, account.ID, fmt.Sprintf("delete-%d", i)))
	}
	issued, err := control.IssueLaunchCode(context.Background(), domain.IssueLaunchCodeCommand{AccountID: account.ID, RouteID: routes[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	insertControlTestRun(t, db, routes[0].ID, "run_aaaaaaaaaaaaaaaaaaaaaaaaaa", domain.RunOnline, domain.DesiredRunning, clock.Now())

	deleted, err := control.DeleteRoute(context.Background(), account.ID, routes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	if deleted.Status != domain.RouteDeleted || deleted.DeletedAt == nil || !deleted.DeletedAt.Equal(now) || deleted.RecoverableUntil == nil || !deleted.RecoverableUntil.Equal(now.Add(7*24*time.Hour)) || deleted.NameReleasedAt != nil {
		t.Fatalf("deleted route = %+v", deleted)
	}
	var status domain.RunStatus
	var desired domain.DesiredState
	var stopRequested, stopped *time.Time
	if err := db.QueryRow(`SELECT status, desired_state, stop_requested_at, stopped_at FROM tunnel_runs WHERE id='run_aaaaaaaaaaaaaaaaaaaaaaaaaa'`).Scan(&status, &desired, &stopRequested, &stopped); err != nil {
		t.Fatal(err)
	}
	if status != domain.RunStopping || desired != domain.DesiredStopped || stopRequested == nil || !stopRequested.Equal(now) || stopped != nil {
		t.Fatalf("run after route delete: status=%q desired=%q requested=%v stopped=%v", status, desired, stopRequested, stopped)
	}
	var revokedAt *time.Time
	if err := db.QueryRow(`SELECT revoked_at FROM route_launch_codes WHERE id=$1`, issued.Code.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt == nil || !revokedAt.Equal(now) {
		t.Fatalf("launch code revoked_at=%v, want %s", revokedAt, now)
	}
	if _, err := control.DeleteRoute(context.Background(), account.ID, routes[0].ID); !errors.Is(err, domain.ErrRouteDeleted) {
		t.Fatalf("second delete error=%v", err)
	}
	if _, err := control.CreateRoute(context.Background(), domain.CreateRouteCommand{AccountID: account.ID, Protocol: "http", Subdomain: "replacement", IdempotencyKey: "replacement"}); err != nil {
		t.Fatalf("deleted route still consumed quota: %v", err)
	}
	var active, deletedCount int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE status='active'), count(*) FILTER (WHERE status='deleted') FROM tunnel_routes WHERE account_id=$1`, account.ID).Scan(&active, &deletedCount); err != nil {
		t.Fatal(err)
	}
	if active != 5 || deletedCount != 1 {
		t.Fatalf("active=%d deleted=%d, want 5 and 1", active, deletedCount)
	}
}

func TestControlRouteRestoreHonorsCutoffAndCapacity(t *testing.T) {
	t.Run("before cutoff", func(t *testing.T) {
		control, db, clock := newControlRouteTestStore(t)
		account := resolveControlTestAccount(t, control, "restore-before")
		route := createControlTestRoute(t, control, account.ID, "restore-before")
		issued, err := control.IssueLaunchCode(context.Background(), domain.IssueLaunchCodeCommand{AccountID: account.ID, RouteID: route.ID})
		if err != nil {
			t.Fatal(err)
		}
		insertControlTestRun(t, db, route.ID, "run_cccccccccccccccccccccccccc", domain.RunStarting, domain.DesiredRunning, clock.Now())
		deleted, err := control.DeleteRoute(context.Background(), account.ID, route.ID)
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(deleted.RecoverableUntil.Add(-time.Microsecond))
		restored, err := control.RestoreRoute(context.Background(), account.ID, route.ID)
		if err != nil || restored.Status != domain.RouteActive || restored.DeletedAt != nil || restored.RecoverableUntil != nil || restored.NameReleasedAt != nil {
			t.Fatalf("restored = %+v, %v", restored, err)
		}
		var desired domain.DesiredState
		var runStatus domain.RunStatus
		var revokedAt *time.Time
		if err := db.QueryRow(`SELECT status, desired_state FROM tunnel_runs WHERE id='run_cccccccccccccccccccccccccc'`).Scan(&runStatus, &desired); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT revoked_at FROM route_launch_codes WHERE id=$1`, issued.Code.ID).Scan(&revokedAt); err != nil {
			t.Fatal(err)
		}
		if runStatus != domain.RunStopping || desired != domain.DesiredStopped || revokedAt == nil {
			t.Fatalf("restore revived stopped state: run=%q desired=%q revoked=%v", runStatus, desired, revokedAt)
		}
	})

	t.Run("at cutoff", func(t *testing.T) {
		control, _, clock := newControlRouteTestStore(t)
		account := resolveControlTestAccount(t, control, "restore-cutoff")
		route := createControlTestRoute(t, control, account.ID, "restore-cutoff")
		deleted, err := control.DeleteRoute(context.Background(), account.ID, route.ID)
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(*deleted.RecoverableUntil)
		if restored, err := control.RestoreRoute(context.Background(), account.ID, route.ID); !errors.Is(err, domain.ErrRouteDeleted) || restored.ID != "" {
			t.Fatalf("cutoff restore = %+v, %v", restored, err)
		}
	})

	t.Run("at capacity", func(t *testing.T) {
		control, _, _ := newControlRouteTestStore(t)
		account := resolveControlTestAccount(t, control, "restore-capacity")
		deletedRoute := createControlTestRoute(t, control, account.ID, "restore-capacity")
		if _, err := control.DeleteRoute(context.Background(), account.ID, deletedRoute.ID); err != nil {
			t.Fatal(err)
		}
		for i := range 5 {
			createControlTestRoute(t, control, account.ID, fmt.Sprintf("capacity-%d", i))
		}
		if restored, err := control.RestoreRoute(context.Background(), account.ID, deletedRoute.ID); !errors.Is(err, domain.ErrRouteLimitReached) || restored.ID != "" {
			t.Fatalf("capacity restore = %+v, %v", restored, err)
		}
	})
}

func TestControlRouteExpiredLabelTransfersAndCannotBeRestored(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	oldOwner := resolveControlTestAccount(t, control, "old-owner")
	newOwner := resolveControlTestAccount(t, control, "new-owner")
	oldRoute := createControlTestRoute(t, control, oldOwner.ID, "transfer-label")
	deleted, err := control.DeleteRoute(context.Background(), oldOwner.ID, oldRoute.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(*deleted.RecoverableUntil)
	newRoute := createControlTestRoute(t, control, newOwner.ID, "transfer-label")
	if newRoute.ID == oldRoute.ID || newRoute.ProxyName == oldRoute.ProxyName || newRoute.AccountID != newOwner.ID {
		t.Fatalf("transferred route did not get an independent identity: old=%+v new=%+v", oldRoute, newRoute)
	}
	var releasedAt *time.Time
	if err := db.QueryRow(`SELECT name_released_at FROM tunnel_routes WHERE id=$1`, oldRoute.ID).Scan(&releasedAt); err != nil {
		t.Fatal(err)
	}
	if releasedAt == nil || !releasedAt.Equal(clock.Now()) {
		t.Fatalf("old name released_at=%v, want %s", releasedAt, clock.Now())
	}
	if restored, err := control.RestoreRoute(context.Background(), oldOwner.ID, oldRoute.ID); !errors.Is(err, domain.ErrRouteDeleted) || restored.ID != "" {
		t.Fatalf("old owner restore = %+v, %v", restored, err)
	}
}

func TestControlRouteReleaseExpiredNamesUsesExactCutoff(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	account := resolveControlTestAccount(t, control, "release-owner")
	first := createControlTestRoute(t, control, account.ID, "release-first")
	second := createControlTestRoute(t, control, account.ID, "release-second")
	firstDeleted, err := control.DeleteRoute(context.Background(), account.ID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(time.Microsecond))
	secondDeleted, err := control.DeleteRoute(context.Background(), account.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(*firstDeleted.RecoverableUntil)
	released, err := control.ReleaseExpiredNames(context.Background(), 10)
	if err != nil || released != 1 {
		t.Fatalf("released=%d, err=%v", released, err)
	}
	var firstReleased, secondReleased *time.Time
	if err := db.QueryRow(`SELECT name_released_at FROM tunnel_routes WHERE id=$1`, first.ID).Scan(&firstReleased); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT name_released_at FROM tunnel_routes WHERE id=$1`, second.ID).Scan(&secondReleased); err != nil {
		t.Fatal(err)
	}
	if firstReleased == nil || !firstReleased.Equal(clock.Now()) || secondReleased != nil || !secondDeleted.RecoverableUntil.After(clock.Now()) {
		t.Fatalf("release boundary first=%v second=%v second cutoff=%v", firstReleased, secondReleased, secondDeleted.RecoverableUntil)
	}
}

func TestControlLaunchIssueStoresOnlyHashExpiresAndRejectsActiveRun(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	owner := resolveControlTestAccount(t, control, "launch-owner")
	foreign := resolveControlTestAccount(t, control, "launch-foreign")
	route := createControlTestRoute(t, control, owner.ID, "launch-route")
	issued, err := control.IssueLaunchCode(context.Background(), domain.IssueLaunchCodeCommand{AccountID: owner.ID, RouteID: route.ID})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || issued.Code.ID == "" || issued.Code.SecretHash != "" || !issued.Code.CreatedAt.Equal(clock.Now()) || !issued.Code.ExpiresAt.Equal(clock.Now().Add(10*time.Minute)) {
		t.Fatalf("issued launch code = %+v token-empty=%t", issued.Code, issued.Token == "")
	}
	parsedID, err := identity.ParseLaunchCredential(issued.Token)
	if err != nil || parsedID != issued.Code.ID {
		t.Fatalf("issued token parsed as %q, %v", parsedID, err)
	}
	var storedHash string
	if err := db.QueryRow(`SELECT secret_hash FROM route_launch_codes WHERE id=$1`, issued.Code.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == issued.Token || !identity.TokenHashEqual(storedHash, identity.HashToken(control.launchPepper, issued.Token)) {
		t.Fatal("launch code storage did not contain only the peppered full-token hash")
	}
	if _, err := control.IssueLaunchCode(context.Background(), domain.IssueLaunchCodeCommand{AccountID: foreign.ID, RouteID: route.ID}); !errors.Is(err, domain.ErrRouteNotFound) {
		t.Fatalf("foreign issue error=%v", err)
	}
	insertControlTestRun(t, db, route.ID, "run_bbbbbbbbbbbbbbbbbbbbbbbbbb", domain.RunStarting, domain.DesiredRunning, clock.Now())
	if result, err := control.IssueLaunchCode(context.Background(), domain.IssueLaunchCodeCommand{AccountID: owner.ID, RouteID: route.ID}); !errors.Is(err, domain.ErrRunAlreadyActive) || result.Token != "" || result.Code.ID != "" {
		t.Fatalf("active-run issue = %+v, %v", result, err)
	}
	if controlTestTableCount(t, db, "route_launch_codes") != 1 {
		t.Fatal("active-run rejection persisted another launch code")
	}
}
