package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func TestControlRunConfirmOfflineRevokesCredentialOnce(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "offline-revocation")
	h.online(t, first)
	proof := controlProof(first)
	if _, err := h.api.RequestCredentialStop(context.Background(), proof); err != nil {
		t.Fatal(err)
	}

	evidence := domain.RunDisconnectEvidence{
		RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOffline: true,
	}
	wantRevokedAt := h.clock.Now().UTC().Truncate(time.Microsecond)
	offline, err := h.api.ConfirmOffline(context.Background(), evidence)
	if err != nil || offline.Status != domain.RunOffline || offline.StoppedAt == nil || !offline.StoppedAt.Equal(wantRevokedAt) {
		t.Fatalf("offline=%#v err=%v", offline, err)
	}
	var stoppedAt time.Time
	var revokedAt sql.NullTime
	if err := h.fixture.DB.QueryRow(`SELECT r.stopped_at,c.revoked_at FROM tunnel_runs r JOIN run_credentials c ON c.run_id=r.id WHERE r.id=$1`, first.Run.ID).Scan(&stoppedAt, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Valid || !stoppedAt.Equal(wantRevokedAt) || !revokedAt.Time.Equal(wantRevokedAt) {
		t.Fatalf("persisted stopped_at=%s revoked_at=%v, want both %s", stoppedAt, revokedAt, wantRevokedAt)
	}

	h.clock.Set(wantRevokedAt.Add(time.Minute))
	heartbeat, err := h.api.Heartbeat(context.Background(), proof)
	if err != nil || !heartbeat.Stopped || heartbeat.Run.Status != domain.RunOffline || heartbeat.Run.StoppedAt == nil || !heartbeat.Run.StoppedAt.Equal(wantRevokedAt) {
		t.Fatalf("terminal heartbeat=%#v err=%v", heartbeat, err)
	}
	if stopped, err := h.api.RequestCredentialStop(context.Background(), proof); err != nil || stopped.Status != domain.RunOffline || stopped.StoppedAt == nil || !stopped.StoppedAt.Equal(wantRevokedAt) {
		t.Fatalf("terminal stop=%#v err=%v", stopped, err)
	}
	if repeated, err := h.api.ConfirmOffline(context.Background(), evidence); err != nil || repeated.Status != domain.RunOffline || repeated.StoppedAt == nil || !repeated.StoppedAt.Equal(wantRevokedAt) {
		t.Fatalf("repeated offline=%#v err=%v", repeated, err)
	}
	if replayed, err := h.api.StartAccountRun(context.Background(), h.startCommand("offline-revocation")); !errors.Is(err, domain.ErrRunStopped) || replayed.CredentialToken != "" {
		t.Fatalf("terminal replay=%#v err=%v", replayed, err)
	}
	if _, err := h.api.ConfirmOnline(context.Background(), domain.RunRegistrationEvidence{
		RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID,
		ConnectedIP: netip.MustParseAddr("198.51.100.8"), ObservedOnline: true,
	}); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("terminal run reconnected: %v", err)
	}
	if err := h.fixture.DB.QueryRow(`SELECT r.stopped_at,c.revoked_at FROM tunnel_runs r JOIN run_credentials c ON c.run_id=r.id WHERE r.id=$1`, first.Run.ID).Scan(&stoppedAt, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Valid || !stoppedAt.Equal(wantRevokedAt) || !revokedAt.Time.Equal(wantRevokedAt) {
		t.Fatalf("repeated terminal calls changed stopped_at=%s revoked_at=%v", stoppedAt, revokedAt)
	}
}

func TestControlRunConfirmOfflineRollsBackWhenCredentialRevocationFails(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "offline-revocation-rollback")
	online := h.online(t, first)
	if online.LeaseExpiresAt == nil {
		t.Fatal("online run has no lease expiry")
	}
	h.clock.Set(*online.LeaseExpiresAt)
	if _, err := h.fixture.DB.Exec(`CREATE FUNCTION reject_run_credential_revocation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'fixture rejects run credential revocation'; END $$;
		CREATE TRIGGER reject_run_credential_revocation BEFORE UPDATE OF revoked_at ON run_credentials
		FOR EACH ROW EXECUTE FUNCTION reject_run_credential_revocation()`); err != nil {
		t.Fatal(err)
	}

	evidence := domain.RunDisconnectEvidence{
		RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOffline: true,
	}
	if _, err := h.api.ConfirmOffline(context.Background(), evidence); err == nil || !strings.Contains(err.Error(), "fixture rejects run credential revocation") {
		t.Fatalf("credential revocation failure=%v", err)
	}
	var status, desiredState string
	var stopRequestedAt, stoppedAt, revokedAt sql.NullTime
	var stopReason sql.NullString
	if err := h.fixture.DB.QueryRow(`SELECT r.status,r.desired_state,r.stop_requested_at,r.stopped_at,r.stop_reason,c.revoked_at
		FROM tunnel_runs r JOIN run_credentials c ON c.run_id=r.id WHERE r.id=$1`, first.Run.ID).
		Scan(&status, &desiredState, &stopRequestedAt, &stoppedAt, &stopReason, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.RunOnline) || desiredState != string(domain.DesiredRunning) || stopRequestedAt.Valid || stoppedAt.Valid || stopReason.Valid || revokedAt.Valid {
		t.Fatalf("failed revocation persisted state: status=%s desired=%s stop_requested=%v stopped=%v reason=%v revoked=%v",
			status, desiredState, stopRequestedAt, stoppedAt, stopReason, revokedAt)
	}

	if _, err := h.fixture.DB.Exec(`DROP TRIGGER reject_run_credential_revocation ON run_credentials;
		DROP FUNCTION reject_run_credential_revocation()`); err != nil {
		t.Fatal(err)
	}
	offline, err := h.api.ConfirmOffline(context.Background(), evidence)
	if err != nil || offline.Status != domain.RunOffline || offline.StoppedAt == nil || !offline.StoppedAt.Equal(*online.LeaseExpiresAt) {
		t.Fatalf("offline retry=%#v err=%v", offline, err)
	}
	if err := h.fixture.DB.QueryRow(`SELECT revoked_at FROM run_credentials WHERE run_id=$1`, first.Run.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Valid || !revokedAt.Time.Equal(*online.LeaseExpiresAt) {
		t.Fatalf("retry revoked_at=%v want=%s", revokedAt, online.LeaseExpiresAt)
	}
}
