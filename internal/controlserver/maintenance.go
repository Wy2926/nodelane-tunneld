package controlserver

import (
	"context"
	"log/slog"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

type maintenanceStore interface {
	Sweep(context.Context, int) (domain.SweepResult, error)
	ReleaseExpiredNames(context.Context, int) (int, error)
}

// Authority expiry precedes evidence-based recovery; names are reusable only
// once the corresponding run is terminal. Readiness is never enabled here.
func runMaintenance(ctx context.Context, ticks <-chan time.Time, store maintenanceStore, reconcilers ...func(context.Context) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-ticks:
			if !open || ctx.Err() != nil {
				return
			}
		}
		batchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, sweepErr := store.Sweep(batchCtx, 200)
		cancel()
		if ctx.Err() != nil {
			return
		}
		var recoveryFailed bool
		for _, reconcile := range reconcilers {
			batchCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
			if reconcile(batchCtx) != nil {
				recoveryFailed = true
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
		}
		batchCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		_, namesErr := store.ReleaseExpiredNames(batchCtx, 200)
		cancel()
		if ctx.Err() == nil && (sweepErr != nil || namesErr != nil || recoveryFailed) {
			slog.Warn("control metadata cleanup incomplete", "code", "dependency_unavailable")
		}
	}
}

func (s *Server) startMaintenance(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.maintenanceCancel = cancel
	s.maintenanceDone = make(chan struct{})
	go func() {
		defer close(s.maintenanceDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		runMaintenance(ctx, ticker.C, s.postgres, s.reconcileRuns...)
	}()
}
