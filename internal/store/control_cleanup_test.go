package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func TestControlCleanupExpiryRetainsSlotUntilVerifiedOffline(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "sweep-start")
	h.clock.Set(first.Run.ConnectDeadlineAt)
	swept, err := h.api.Sweep(context.Background(), 10)
	if err != nil || swept.ExpiredRuns != 1 || swept.DeletedRuns != 0 {
		t.Fatalf("expiry sweep=%#v err=%v", swept, err)
	}
	if _, err := h.api.AuthorizeRun(context.Background(), controlProof(first)); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("swept credential authorized: %v", err)
	}
	got, err := h.api.StartAccountRun(context.Background(), h.startCommand("blocked-replacement"))
	requireControlZeroStart(t, got, err)
	if !errors.Is(err, domain.ErrRunAlreadyActive) {
		t.Fatalf("sweep released active slot: %v", err)
	}
	h.clock.Set(first.Run.ConnectDeadlineAt.Add(time.Hour))
	swept, err = h.api.Sweep(context.Background(), 10)
	if err != nil || swept.ExpiredRuns != 0 || swept.DeletedRuns != 0 {
		t.Fatalf("unverified run deleted: %#v %v", swept, err)
	}
	if controlTestTableCount(t, h.fixture.DB, "run_credentials") != 1 {
		t.Fatal("active credential deleted")
	}
	evidence := domain.RunDisconnectEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOffline: true}
	offline, err := h.api.ConfirmOffline(context.Background(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Set(offline.StoppedAt.Add(2*time.Minute - time.Microsecond))
	if swept, err := h.api.Sweep(context.Background(), 10); err != nil || swept.DeletedRuns != 0 {
		t.Fatalf("early terminal cleanup: %#v %v", swept, err)
	}
	h.clock.Set(offline.StoppedAt.Add(2 * time.Minute))
	swept, err = h.api.Sweep(context.Background(), 10)
	if err != nil || swept.DeletedRuns != 1 {
		t.Fatalf("exact terminal cleanup: %#v %v", swept, err)
	}
	requireControlCounts(t, h.fixture.DB, 0, 0, 0)
	if controlTestTableCount(t, h.fixture.DB, "tunnel_routes") != 1 {
		t.Fatal("cleanup deleted route")
	}
}

func TestControlCleanupRetainsExpiredLaunchHashForValidReplay(t *testing.T) {
	h := newControlRunHarness(t)
	cmd := h.launch(t)
	h.clock.Set(h.route.CreatedAt.Add(9*time.Minute + 59*time.Second))
	first, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Set(h.route.CreatedAt.Add(10*time.Minute + time.Second))
	swept, err := h.api.Sweep(context.Background(), 10)
	if err != nil || swept.DeletedCodes != 0 {
		t.Fatalf("valid replay lost launch hash: %#v %v", swept, err)
	}
	got, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil || got.CredentialToken != first.CredentialToken {
		t.Fatalf("replay after cleanup failed: %v", err)
	}
	h.clock.Set(h.route.CreatedAt.Add(11*time.Minute + 59*time.Second))
	swept, err = h.api.Sweep(context.Background(), 10)
	if err != nil || swept.DeletedCodes != 1 || swept.DeletedReplays != 1 || swept.DeletedRuns != 0 {
		t.Fatalf("expired replay/code cleanup=%#v %v", swept, err)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 0)
}

func TestControlCleanupRetainsOfflineCredentialThroughEveryReplayWindow(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "retention")
	if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.ConfirmOffline(context.Background(), domain.RunDisconnectEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOffline: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.fixture.DB.Exec(`UPDATE operation_replays SET expires_at=expires_at+interval '30 seconds'`); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(h.route.CreatedAt.Add(2 * time.Minute))
	swept, err := h.api.Sweep(context.Background(), 10)
	if err != nil || swept.DeletedRuns != 0 {
		t.Fatalf("unexpired replay credential deleted: %#v %v", swept, err)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 1)
	h.clock.Set(h.route.CreatedAt.Add(150 * time.Second))
	swept, err = h.api.Sweep(context.Background(), 10)
	if err != nil || swept.DeletedRuns != 1 || swept.DeletedReplays != 1 {
		t.Fatalf("all windows elapsed cleanup=%#v %v", swept, err)
	}
	requireControlCounts(t, h.fixture.DB, 0, 0, 0)
}

func TestControlCleanupLimitBoundsMutationAndRejectsInvalidLimit(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "bounded")
	h.launch(t)
	h.clock.Set(first.Run.ConnectDeadlineAt.Add(10 * time.Minute))
	if _, err := h.api.Sweep(context.Background(), 0); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("zero limit=%v", err)
	}
	swept, err := h.api.Sweep(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if swept.ExpiredRuns+swept.DeletedRuns+swept.DeletedCodes+swept.DeletedReplays > 1 {
		t.Fatalf("sweep exceeded bound: %#v", swept)
	}
	if swept.ExpiredRuns != 1 {
		t.Fatalf("run expiry must be prioritized: %#v", swept)
	}
	if _, err := h.api.Sweep(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
}

func TestControlCleanupConcurrentSweepsDeleteEachTerminalRunOnce(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "cleanup-race")
	if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.ConfirmOffline(context.Background(), domain.RunDisconnectEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOffline: true}); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(h.route.CreatedAt.Add(2 * time.Minute))
	const workers = 6
	type outcome struct {
		result domain.SweepResult
		err    error
	}
	gate := make(chan struct{})
	done := make(chan outcome, workers)
	for range workers {
		go func() { <-gate; result, err := h.api.Sweep(context.Background(), 10); done <- outcome{result, err} }()
	}
	close(gate)
	var deletedRuns, deletedReplays int
	for range workers {
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		deletedRuns += got.result.DeletedRuns
		deletedReplays += got.result.DeletedReplays
	}
	if deletedRuns != 1 || deletedReplays != 1 {
		t.Fatalf("cleanup duplicated terminal deletion: runs=%d replays=%d", deletedRuns, deletedReplays)
	}
	requireControlCounts(t, h.fixture.DB, 0, 0, 0)
}

func TestControlCleanupRevalidatesLeaseAfterDiscovery(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "cleanup-revalidate")
	h.online(t, first)
	h.clock.Set(h.route.CreatedAt.Add(90 * time.Second))
	err := controlRunWithAccountLockWait(t, h, h.route.CreatedAt.Add(89*time.Second), func(api controlRunOperations, ctx context.Context) error {
		_, err := api.Sweep(ctx, 1)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.AuthorizeRun(context.Background(), controlProof(first)); err != nil {
		t.Fatalf("sweep did not revalidate after candidate discovery: %v", err)
	}
}

func TestControlCleanupResamplesTimeAfterFinalReplayLock(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "cleanup-final-lock")
	if _, err := h.api.RequestCredentialStop(context.Background(), controlProof(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.ConfirmOffline(context.Background(), domain.RunDisconnectEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOffline: true}); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(h.route.CreatedAt.Add(150 * time.Second))
	var result domain.SweepResult
	err := controlRunWithLockWait(t, h, h.route.CreatedAt.Add(3*time.Minute),
		`UPDATE operation_replays SET expires_at=$2 WHERE run_id=$1 RETURNING id`, []any{first.Run.ID, h.route.CreatedAt.Add(3 * time.Minute)}, "FROM operation_replays",
		func(api controlRunOperations, ctx context.Context) error {
			var err error
			result, err = api.Sweep(ctx, 1)
			return err
		})
	if err != nil || result.DeletedRuns != 1 || result.DeletedReplays != 1 {
		t.Fatalf("cleanup used clock from before final replay lock: %#v %v", result, err)
	}
	requireControlCounts(t, h.fixture.DB, 0, 0, 0)
}
