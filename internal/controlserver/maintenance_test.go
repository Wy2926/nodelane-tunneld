package controlserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

type maintenanceProbe struct {
	sweep func(context.Context, int) (domain.SweepResult, error)
	names func(context.Context, int) (int, error)
}

func (p maintenanceProbe) Sweep(ctx context.Context, limit int) (domain.SweepResult, error) {
	return p.sweep(ctx, limit)
}
func (p maintenanceProbe) ReleaseExpiredNames(ctx context.Context, limit int) (int, error) {
	return p.names(ctx, limit)
}

func TestMaintenanceUsesBoundedBatchesAndRecoversAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	called := make(chan string, 4)
	check := func(ctx context.Context, limit int) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second || limit < 1 || limit > 1000 {
			t.Error("unbounded maintenance operation")
		}
	}
	probe := maintenanceProbe{
		sweep: func(ctx context.Context, limit int) (domain.SweepResult, error) {
			check(ctx, limit)
			called <- "sweep"
			return domain.SweepResult{}, errors.New("private database error")
		},
		names: func(ctx context.Context, limit int) (int, error) { check(ctx, limit); called <- "names"; return 0, nil },
	}
	done := make(chan struct{})
	go func() { runMaintenance(ctx, ticks, probe); close(done) }()
	for i := 0; i < 2; i++ {
		ticks <- time.Now()
		for _, want := range []string{"sweep", "names"} {
			select {
			case got := <-called:
				if got != want {
					t.Errorf("got=%s want=%s", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("maintenance failed to advance")
			}
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not stop")
	}
}

func TestMaintenanceCancellationInterruptsInflightDatabaseWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	started := make(chan struct{})
	probe := maintenanceProbe{
		sweep: func(ctx context.Context, _ int) (domain.SweepResult, error) {
			close(started)
			<-ctx.Done()
			return domain.SweepResult{}, ctx.Err()
		},
		names: func(context.Context, int) (int, error) {
			t.Error("cleanup continued after cancellation")
			return 0, nil
		},
	}
	done := make(chan struct{})
	go func() { runMaintenance(ctx, ticks, probe); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("database operation outlived service")
	}
}
