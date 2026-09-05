package domain

import (
	"testing"
	"time"
)

func TestRouteRecoverableAtRequiresDeletedUnreleasedRouteBeforeCutoff(t *testing.T) {
	deadline := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	releasedAt := deadline.Add(-time.Hour)

	tests := []struct {
		name  string
		route Route
		now   time.Time
		want  bool
	}{
		{
			name:  "immediately before cutoff",
			route: Route{Status: RouteDeleted, RecoverableUntil: &deadline},
			now:   deadline.Add(-time.Nanosecond),
			want:  true,
		},
		{
			name:  "at cutoff",
			route: Route{Status: RouteDeleted, RecoverableUntil: &deadline},
			now:   deadline,
			want:  false,
		},
		{
			name:  "after cutoff",
			route: Route{Status: RouteDeleted, RecoverableUntil: &deadline},
			now:   deadline.Add(time.Nanosecond),
			want:  false,
		},
		{
			name:  "missing cutoff",
			route: Route{Status: RouteDeleted},
			now:   deadline.Add(-time.Hour),
			want:  false,
		},
		{
			name:  "active route",
			route: Route{Status: RouteActive, RecoverableUntil: &deadline},
			now:   deadline.Add(-time.Hour),
			want:  false,
		},
		{
			name:  "released name",
			route: Route{Status: RouteDeleted, RecoverableUntil: &deadline, NameReleasedAt: &releasedAt},
			now:   deadline.Add(-time.Hour),
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.route.RecoverableAt(tt.now); got != tt.want {
				t.Fatalf("RecoverableAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunOccupiesActiveSlotForNonterminalLifecycleStates(t *testing.T) {
	tests := []struct {
		status RunStatus
		want   bool
	}{
		{status: RunStarting, want: true},
		{status: RunOnline, want: true},
		{status: RunStopping, want: true},
		{status: RunOffline, want: false},
		{status: RunStatus("unknown"), want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := (Run{Status: tt.status}).OccupiesActiveSlot(); got != tt.want {
				t.Fatalf("OccupiesActiveSlot() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunAllowsConnectionBeforeInitialDeadline(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Minute)
	leaseExpiry := now.Add(-time.Minute)
	route := Route{ID: "rte_demo", Status: RouteActive}
	run := Run{
		ID:                "run_demo",
		RouteID:           route.ID,
		Status:            RunStarting,
		DesiredState:      DesiredRunning,
		ConnectDeadlineAt: deadline,
		LeaseExpiresAt:    &leaseExpiry,
	}
	credential := RunCredential{ID: "rc_demo", RunID: run.ID}

	if !run.AllowsConnectionAt(route, credential, now) {
		t.Fatal("unconnected starting run should use its future connection deadline, not its stale lease")
	}
	if run.AllowsConnectionAt(route, credential, deadline) {
		t.Fatal("initial connection deadline must be exclusive")
	}
}

func TestRunAllowsReconnectionBeforeLeaseDeadline(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	connectedAt := now.Add(-time.Minute)
	leaseExpiry := now.Add(time.Minute)
	route := Route{ID: "rte_demo", Status: RouteActive}
	run := Run{
		ID:                "run_demo",
		RouteID:           route.ID,
		Status:            RunOnline,
		DesiredState:      DesiredRunning,
		ConnectedAt:       &connectedAt,
		ConnectDeadlineAt: now.Add(-time.Minute),
		LeaseExpiresAt:    &leaseExpiry,
	}
	credential := RunCredential{ID: "rc_demo", RunID: run.ID}

	if !run.AllowsConnectionAt(route, credential, now) {
		t.Fatal("connected online run should use its future lease deadline, not its expired initial deadline")
	}
	if run.AllowsConnectionAt(route, credential, leaseExpiry) {
		t.Fatal("lease deadline must be exclusive")
	}
}

func TestRunAllowsConnectionAtRejectsInvalidStateIndependently(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	connectedAt := now.Add(-time.Minute)
	leaseExpiry := now.Add(time.Minute)
	revokedAt := now.Add(-time.Second)
	validRoute := Route{ID: "rte_demo", Status: RouteActive}
	validRun := Run{
		ID:                "run_demo",
		RouteID:           validRoute.ID,
		Status:            RunOnline,
		DesiredState:      DesiredRunning,
		ConnectedAt:       &connectedAt,
		ConnectDeadlineAt: now.Add(time.Minute),
		LeaseExpiresAt:    &leaseExpiry,
	}
	validCredential := RunCredential{ID: "rc_demo", RunID: validRun.ID}

	tests := []struct {
		name       string
		route      Route
		run        Run
		credential RunCredential
	}{
		{name: "route mismatch", route: Route{ID: "rte_other", Status: RouteActive}, run: validRun, credential: validCredential},
		{name: "credential run mismatch", route: validRoute, run: validRun, credential: RunCredential{ID: "rc_demo", RunID: "run_other"}},
		{name: "empty route identity", route: Route{Status: RouteActive}, run: validRun, credential: validCredential},
		{name: "empty run identity", route: validRoute, run: Run{RouteID: validRoute.ID, Status: RunOnline, DesiredState: DesiredRunning, ConnectedAt: &connectedAt, LeaseExpiresAt: &leaseExpiry}, credential: validCredential},
		{name: "empty credential identity", route: validRoute, run: validRun, credential: RunCredential{RunID: validRun.ID}},
		{name: "deleted route", route: Route{ID: validRoute.ID, Status: RouteDeleted}, run: validRun, credential: validCredential},
		{name: "desired stopped", route: validRoute, run: func() Run { r := validRun; r.DesiredState = DesiredStopped; return r }(), credential: validCredential},
		{name: "stopping run", route: validRoute, run: func() Run { r := validRun; r.Status = RunStopping; return r }(), credential: validCredential},
		{name: "offline run", route: validRoute, run: func() Run { r := validRun; r.Status = RunOffline; return r }(), credential: validCredential},
		{name: "unknown run status", route: validRoute, run: func() Run { r := validRun; r.Status = RunStatus("unknown"); return r }(), credential: validCredential},
		{name: "revoked credential", route: validRoute, run: validRun, credential: RunCredential{ID: validCredential.ID, RunID: validRun.ID, RevokedAt: &revokedAt}},
		{name: "established run missing lease deadline", route: validRoute, run: func() Run { r := validRun; r.LeaseExpiresAt = nil; return r }(), credential: validCredential},
		{name: "established run expired lease", route: validRoute, run: func() Run { r := validRun; expired := now.Add(-time.Nanosecond); r.LeaseExpiresAt = &expired; return r }(), credential: validCredential},
		{name: "online run without first connection", route: validRoute, run: func() Run { r := validRun; r.ConnectedAt = nil; return r }(), credential: validCredential},
		{name: "unconnected run missing initial deadline", route: validRoute, run: func() Run {
			r := validRun
			r.Status = RunStarting
			r.ConnectedAt = nil
			r.ConnectDeadlineAt = time.Time{}
			return r
		}(), credential: validCredential},
		{name: "unconnected run expired initial deadline despite future lease", route: validRoute, run: func() Run {
			r := validRun
			r.Status = RunStarting
			r.ConnectedAt = nil
			r.ConnectDeadlineAt = now.Add(-time.Nanosecond)
			return r
		}(), credential: validCredential},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.run.AllowsConnectionAt(tt.route, tt.credential, now) {
				t.Fatal("AllowsConnectionAt() = true, want false")
			}
		})
	}
}
