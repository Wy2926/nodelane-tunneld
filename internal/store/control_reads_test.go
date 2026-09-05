package store

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

func TestControlRouteViewsAreOwnedAndOnlyIncludeCurrentRuns(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	ctx := context.Background()
	owner := resolveControlTestAccount(t, control, "read-owner")
	other := resolveControlTestAccount(t, control, "read-other")
	active := createControlTestRoute(t, control, owner.ID, "read-active")
	inactive := createControlTestRoute(t, control, owner.ID, "read-inactive")
	deleted := createControlTestRoute(t, control, owner.ID, "read-deleted")
	foreign := createControlTestRoute(t, control, other.ID, "read-foreign")
	if _, err := control.DeleteRoute(ctx, owner.ID, deleted.ID); err != nil {
		t.Fatal(err)
	}
	runID, err := identity.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	insertControlTestRun(t, db, active.ID, runID, domain.RunOnline, domain.DesiredRunning, clock.Now())
	oldRunID, err := identity.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	insertControlTestRun(t, db, inactive.ID, oldRunID, domain.RunStarting, domain.DesiredRunning, clock.Now())
	if _, err := db.Exec(`UPDATE tunnel_runs SET status='offline',desired_state='stopped',stopped_at=$2 WHERE id=$1`, oldRunID, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(time.Hour))
	views, err := control.ListRouteViews(ctx, owner.ID, domain.RouteQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("owned active routes: %+v", views)
	}
	for _, view := range views {
		switch view.Route.ID {
		case active.ID:
			if view.CurrentRun == nil || view.CurrentRun.ID != runID || view.CurrentRun.LastHeartbeatAt != nil || view.CurrentRun.Status != domain.RunOnline {
				t.Fatalf("current run metadata changed by read: %+v", view.CurrentRun)
			}
		case inactive.ID:
			if view.CurrentRun != nil {
				t.Fatalf("offline history exposed: %+v", view.CurrentRun)
			}
		default:
			t.Fatalf("unowned/deleted route exposed: %+v", view)
		}
	}
	deletedViews, err := control.ListRouteViews(ctx, owner.ID, domain.RouteQuery{Deleted: true})
	if err != nil || len(deletedViews) != 1 || deletedViews[0].Route.ID != deleted.ID {
		t.Fatalf("deleted routes: %+v, %v", deletedViews, err)
	}
	for _, id := range []string{foreign.ID, "rte_missing"} {
		if _, err := control.GetRouteView(ctx, owner.ID, id); !errors.Is(err, domain.ErrRouteNotFound) {
			t.Fatalf("foreign/missing route error = %v", err)
		}
	}
	view, err := control.GetRouteView(ctx, owner.ID, deleted.ID)
	if err != nil || view.Route.Status != domain.RouteDeleted || view.CurrentRun != nil {
		t.Fatalf("deleted detail: %+v, %v", view, err)
	}
	var heartbeat *time.Time
	if err := db.QueryRow(`SELECT last_heartbeat_at FROM tunnel_runs WHERE id=$1`, runID).Scan(&heartbeat); err != nil || heartbeat != nil {
		t.Fatalf("read renewed run: %v %v", heartbeat, err)
	}
}

func TestControlRouteViewUsesConsistentReadSnapshot(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	owner := resolveControlTestAccount(t, control, "snapshot-owner")
	route := createControlTestRoute(t, control, owner.ID, "snapshot-route")
	runID, err := identity.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	insertControlTestRun(t, db, route.ID, runID, domain.RunStarting, domain.DesiredRunning, clock.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `LOCK TABLE tunnel_runs IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	pid := controlTestBlockerPID(t, blocker)
	type outcome struct {
		view domain.RouteView
		err  error
	}
	done := make(chan outcome, 1)
	go func() { view, err := control.GetRouteView(ctx, owner.ID, route.ID); done <- outcome{view, err} }()
	waitForControlLockWait(t, db, pid)
	if _, err := blocker.ExecContext(ctx, `UPDATE tunnel_routes SET status='deleted',deleted_at=$2,recoverable_until=$3 WHERE id=$1`, route.ID, clock.Now(), clock.Now().Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE tunnel_runs SET status='stopping',desired_state='stopped',stop_requested_at=$2 WHERE id=$1`, runID, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.view.Route.Status != domain.RouteActive || got.view.CurrentRun == nil || got.view.CurrentRun.Status != domain.RunStarting {
		t.Fatalf("mixed route/run snapshots: %+v, %v", got.view, got.err)
	}
}

func TestControlIPBanReadsApplyScopeNetworkAndExpiry(t *testing.T) {
	control, db, clock := newControlRouteTestStore(t)
	now := clock.Now()
	for _, ban := range []struct {
		id, network, scope string
		expiry             any
	}{
		{"client-v4", "192.0.2.0/24", "tunnel_client", nil},
		{"visitor-v4", "198.51.100.0/24", "public_visitor", nil},
		{"both-v6", "2001:db8::/64", "both", now.Add(time.Hour)},
		{"expired", "203.0.113.0/24", "both", now},
	} {
		if _, err := db.Exec(`INSERT INTO network_bans(id,network,scope,reason,expires_at,created_at) VALUES ($1,$2,$3,'test',$4,$5)`, ban.id, ban.network, ban.scope, ban.expiry, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		ip, scope string
		want      bool
	}{
		{"192.0.2.3", "tunnel_client", true},
		{"::ffff:192.0.2.3", "tunnel_client", true},
		{"192.0.2.3", "public_visitor", false},
		{"198.51.100.3", "tunnel_client", false},
		{"198.51.100.3", "public_visitor", true},
		{"2001:db8::3", "tunnel_client", true},
		{"2001:db8::3", "public_visitor", true},
		{"2001:db8:0:1::3", "tunnel_client", false},
		{"203.0.113.3", "tunnel_client", false},
	} {
		got, err := control.IsIPBanned(context.Background(), netip.MustParseAddr(tc.ip), tc.scope, now)
		if err != nil || got != tc.want {
			t.Errorf("ban %s/%s = %t, %v", tc.ip, tc.scope, got, err)
		}
	}
	for _, ip := range []netip.Addr{{}, netip.MustParseAddr("fe80::1%eth0"), netip.IPv4Unspecified()} {
		if _, err := control.IsIPBanned(context.Background(), ip, "tunnel_client", now); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("invalid IP error = %v", err)
		}
	}
	if _, err := control.IsIPBanned(context.Background(), netip.MustParseAddr("192.0.2.3"), "unknown", now); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("invalid scope error = %v", err)
	}
}
