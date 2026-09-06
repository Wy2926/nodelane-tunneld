package runclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Wait(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

type testEngine struct {
	closed  chan struct{}
	once    sync.Once
	started chan struct{}
	onClose func()
}

func newTestEngine() *testEngine {
	return &testEngine{closed: make(chan struct{}), started: make(chan struct{})}
}
func (e *testEngine) Run(ctx context.Context) error {
	close(e.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.closed:
		return nil
	}
}
func (e *testEngine) Close() {
	e.once.Do(func() {
		if e.onClose != nil {
			e.onClose()
		}
		close(e.closed)
	})
}

type testBackend struct {
	heartbeat func(context.Context, string, string) (HeartbeatResult, error)
	stop      func(context.Context, string, string) (Run, error)
}

type unconfirmedEngine struct{ started, release, finished chan struct{} }

func (e *unconfirmedEngine) Run(context.Context) error {
	close(e.started)
	<-e.release
	close(e.finished)
	return nil
}
func (e *unconfirmedEngine) Close() {}

func TestRunnerUnconfirmedCloseNeverReportsStopped(t *testing.T) {
	bootstrap := testBootstrap(t)
	engine := &unconfirmedEngine{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	defer func() { close(engine.release); <-engine.finished }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	var statuses []Status
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(string) (Engine, error) { return engine, nil }, CloseTimeout: 30 * time.Millisecond, StopTimeout: 30 * time.Millisecond, OnStatus: func(status Status) { statuses = append(statuses, status) }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, bootstrap, initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}) }()
	<-engine.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCloseUnconfirmed) {
			t.Fatalf("unconfirmed local close was hidden: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unconfirmed close blocked exit")
	}
	for _, status := range statuses {
		if status.State == StatusStopped {
			t.Fatal("reported stopped while engine was still running")
		}
	}
	last := statuses[len(statuses)-1]
	if last.State != StatusStopping || last.Code != "close_unconfirmed" {
		t.Fatalf("unconfirmed final state: %+v", last)
	}
}

func (b testBackend) Heartbeat(ctx context.Context, id, token string) (HeartbeatResult, error) {
	return b.heartbeat(ctx, id, token)
}
func (b testBackend) Stop(ctx context.Context, id, token string) (Run, error) {
	return b.stop(ctx, id, token)
}

func initialRun(now time.Time) Run {
	return Run{ID: "anr_test", ProxyName: "anp_test", Protocol: "http", PublicEndpoint: "anon-example.tunnel.test", CredentialToken: "run-secret", CreatedAt: now, ConnectDeadlineAt: now.Add(2 * time.Minute), HardExpiresAt: now.Add(time.Hour)}
}

func clockRunner(t *testing.T, clock *testClock, backend testBackend, engine *testEngine, onStatus func(Status)) *Runner {
	t.Helper()
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(string) (Engine, error) { return engine, nil }, Now: clock.Now, Wait: clock.Wait, Jitter: func(d time.Duration) time.Duration { return d }, OnStatus: onStatus})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestRunnerDoesNotExtendOriginalFirstConnectDeadline(t *testing.T) {
	bootstrap := testBootstrap(t)
	start := time.Now()
	clock := &testClock{now: start}
	engine := newTestEngine()
	var calls, stops int
	backend := testBackend{heartbeat: func(_ context.Context, id, token string) (HeartbeatResult, error) {
		calls++
		if id != "anr_test" || token != "run-secret" {
			t.Error("wrong heartbeat proof")
		}
		return HeartbeatResult{Run: Run{ID: id, DesiredState: "running", ConnectDeadlineAt: clock.Now().Add(2 * time.Minute), HardExpiresAt: start.Add(time.Hour)}}, nil
	}, stop: func(ctx context.Context, id, token string) (Run, error) {
		stops++
		if ctx.Err() != nil || id != "anr_test" || token != "run-secret" {
			t.Error("cleanup did not get independent credential context")
		}
		return Run{}, nil
	}}
	runner := clockRunner(t, clock, backend, engine, nil)
	err := runner.Run(context.Background(), bootstrap, initialRun(start), Target{"http", "127.0.0.1", 3000})
	if !errors.Is(err, ErrConnectDeadline) || !clock.Now().Equal(start.Add(2*time.Minute)) || calls < 1 || stops != 1 {
		t.Fatalf("initial deadline extended: err=%v elapsed=%v calls=%d stops=%d", err, clock.Now().Sub(start), calls, stops)
	}
	select {
	case <-engine.closed:
	default:
		t.Fatal("frpc survived original deadline")
	}
}

func TestRunnerUsesLeaseAfterOnlineButNeverExtendsOriginalHardExpiry(t *testing.T) {
	bootstrap := testBootstrap(t)
	start := time.Now()
	clock := &testClock{now: start}
	engine := newTestEngine()
	var online bool
	backend := testBackend{heartbeat: func(_ context.Context, id, _ string) (HeartbeatResult, error) {
		return HeartbeatResult{Run: Run{ID: id, DesiredState: "running", LeaseExpiresAt: clock.Now().Add(90 * time.Second), HardExpiresAt: clock.Now().Add(time.Hour)}}, nil
	}, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	runner := clockRunner(t, clock, backend, engine, func(status Status) {
		if status.State == StatusOnline {
			online = true
		}
		if strings.Contains(fmt.Sprintf("%+v", status), "run-secret") {
			t.Error("status leaked run credential")
		}
	})
	err := runner.Run(context.Background(), bootstrap, initialRun(start), Target{"http", "127.0.0.1", 3000})
	if !errors.Is(err, ErrHardExpired) || !clock.Now().Equal(start.Add(time.Hour)) || !online {
		t.Fatalf("hard expiry/online: err=%v elapsed=%v online=%v", err, clock.Now().Sub(start), online)
	}
}

func TestRunnerHeartbeatFailuresCannotOutliveLastConfirmedLease(t *testing.T) {
	bootstrap := testBootstrap(t)
	start := time.Now()
	clock := &testClock{now: start}
	engine := newTestEngine()
	calls := 0
	backend := testBackend{heartbeat: func(_ context.Context, id, _ string) (HeartbeatResult, error) {
		calls++
		if calls == 1 {
			return HeartbeatResult{Run: Run{ID: id, DesiredState: "running", LeaseExpiresAt: start.Add(95 * time.Second), HardExpiresAt: start.Add(time.Hour)}}, nil
		}
		return HeartbeatResult{}, errors.New("network secret https://secret.test")
	}, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	runner := clockRunner(t, clock, backend, engine, nil)
	err := runner.Run(context.Background(), bootstrap, initialRun(start), Target{"http", "127.0.0.1", 3000})
	if !errors.Is(err, ErrLeaseExpired) || !clock.Now().Equal(start.Add(95*time.Second)) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("lease failure: %v elapsed=%v", err, clock.Now().Sub(start))
	}
}

func TestRunnerRemoteStopAndUnauthorizedCloseEnginePromptly(t *testing.T) {
	for _, mode := range []string{"desired", "401", "410"} {
		t.Run(mode, func(t *testing.T) {
			bootstrap := testBootstrap(t)
			start := time.Now()
			clock := &testClock{now: start}
			engine := newTestEngine()
			backend := testBackend{heartbeat: func(_ context.Context, id, _ string) (HeartbeatResult, error) {
				if mode == "desired" {
					return HeartbeatResult{Run: Run{ID: id, DesiredState: "stopped"}, Stopped: true}, nil
				}
				status := 401
				if mode == "410" {
					status = 410
				}
				return HeartbeatResult{}, &APIError{StatusCode: status, Code: "unauthorized"}
			}, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
			err := clockRunner(t, clock, backend, engine, nil).Run(context.Background(), bootstrap, initialRun(start), Target{"http", "127.0.0.1", 3000})
			if !errors.Is(err, ErrRunStopped) || clock.Now().Sub(start) > 5*time.Second {
				t.Fatalf("remote stop delayed: %v %v", err, clock.Now().Sub(start))
			}
			select {
			case <-engine.closed:
			default:
				t.Fatal("engine survived remote stop")
			}
		})
	}
}

func TestRunnerCancellationClosesLocallyBeforeBoundedStopReport(t *testing.T) {
	bootstrap := testBootstrap(t)
	engine := newTestEngine()
	ctx, cancel := context.WithCancel(context.Background())
	var stops atomic.Int32
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(stopCtx context.Context, id, token string) (Run, error) {
		stops.Add(1)
		select {
		case <-engine.closed:
		default:
			t.Error("stop report began before local close")
		}
		if stopCtx.Err() != nil || id != "anr_test" || token != "run-secret" {
			t.Error("stop credential context invalid")
		}
		<-stopCtx.Done()
		return Run{}, errors.New("stop-secret")
	}}
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(string) (Engine, error) { return engine, nil }, StopTimeout: 50 * time.Millisecond, CloseTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, bootstrap, initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}) }()
	<-engine.started
	before := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop report blocked local exit")
	}
	if time.Since(before) > 500*time.Millisecond || stops.Load() != 1 {
		t.Fatal("cancel cleanup was not bounded")
	}
}

func TestRunnerTemporaryCAContainsOnlyPublicCertificateAndIsRemoved(t *testing.T) {
	bootstrap := testBootstrap(t)
	engine := newTestEngine()
	var path string
	ctx, cancel := context.WithCancel(context.Background())
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(configText string) (Engine, error) {
		var config v1.ClientConfig
		if err := frpconfig.LoadConfigure([]byte(configText), &config, true, "toml"); err != nil {
			t.Fatal(err)
		}
		path = config.Transport.TLS.TrustedCaFile
		engine.onClose = func() {
			if _, err := os.Stat(path); err != nil {
				t.Error("temporary CA removed before engine close")
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != bootstrap.FRP.TrustedCAPEM || strings.Contains(string(data), "run-secret") {
			t.Error("temporary CA contains non-public data")
		}
		cancel()
		return engine, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, bootstrap, initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if path == "" {
		t.Fatal("engine did not receive CA path")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary CA remains: %v", err)
	}
}

func TestRunnerExpiredAllocationNeverStartsNativeEngine(t *testing.T) {
	bootstrap := testBootstrap(t)
	var starts, stops int
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(context.Context, string, string) (Run, error) { stops++; return Run{}, nil }}
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(string) (Engine, error) { starts++; return newTestEngine(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	run := initialRun(time.Now().Add(-3 * time.Minute))
	err = runner.Run(context.Background(), bootstrap, run, Target{"http", "127.0.0.1", 3000})
	if !errors.Is(err, ErrConnectDeadline) || starts != 0 || stops != 1 {
		t.Fatalf("expired allocation opened frpc: err=%v starts=%d stops=%d", err, starts, stops)
	}
}

func TestRunnerRejectsUnboundedDurationsAndTypedNilBackend(t *testing.T) {
	backend := testBackend{}
	var typedNil *Client
	for _, options := range []RunnerOptions{{}, {Backend: typedNil}, {Backend: backend, HeartbeatInterval: time.Minute}, {Backend: backend, StopTimeout: time.Hour}, {Backend: backend, CloseTimeout: -time.Second}, {Backend: backend, ProxyURL: "http://secret@proxy.test:80/path"}} {
		if _, err := NewRunner(options); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid runner options: %v", err)
		}
	}
}

func TestRunnerCapsLeaseExtensionAndIgnoresLaterEmptyLease(t *testing.T) {
	bootstrap := testBootstrap(t)
	start := time.Now()
	clock := &testClock{now: start}
	engine := newTestEngine()
	calls := 0
	backend := testBackend{heartbeat: func(_ context.Context, id, _ string) (HeartbeatResult, error) {
		calls++
		lease := time.Time{}
		if calls == 1 {
			lease = start.Add(24 * time.Hour)
		}
		return HeartbeatResult{Run: Run{ID: id, DesiredState: "running", LeaseExpiresAt: lease, HardExpiresAt: start.Add(time.Hour)}}, nil
	}, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	err := clockRunner(t, clock, backend, engine, nil).Run(context.Background(), bootstrap, initialRun(start), Target{"http", "127.0.0.1", 3000})
	if !errors.Is(err, ErrLeaseExpired) || !clock.Now().Equal(start.Add(95*time.Second)) {
		t.Fatalf("lease was not capped or empty lease reset it: %v elapsed=%v", err, clock.Now().Sub(start))
	}
}

func TestRunnerDeadlineInterruptsBlockedHeartbeat(t *testing.T) {
	bootstrap := testBootstrap(t)
	engine := newTestEngine()
	var beats atomic.Int32
	backend := testBackend{heartbeat: func(ctx context.Context, _, _ string) (HeartbeatResult, error) {
		beats.Add(1)
		<-ctx.Done()
		return HeartbeatResult{}, ctx.Err()
	}, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(string) (Engine, error) { return engine, nil }, HeartbeatInterval: time.Millisecond, Jitter: func(d time.Duration) time.Duration { return d }})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	run := initialRun(start)
	run.ConnectDeadlineAt = start.Add(50 * time.Millisecond)
	err = runner.Run(context.Background(), bootstrap, run, Target{"http", "127.0.0.1", 3000})
	if !errors.Is(err, ErrConnectDeadline) || time.Since(start) > 500*time.Millisecond || beats.Load() != 1 {
		t.Fatalf("blocked heartbeat escaped deadline: err=%v elapsed=%v beats=%d", err, time.Since(start), beats.Load())
	}
}

func TestRunnerInvalidTrustNeverStartsEngineAndSanitizesEngineFailure(t *testing.T) {
	bootstrap := testBootstrap(t)
	var starts, stops int
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(context.Context, string, string) (Run, error) { stops++; return Run{}, nil }}
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(string) (Engine, error) { starts++; return nil, errors.New("raw-frp run-secret") }})
	if err != nil {
		t.Fatal(err)
	}
	invalid := bootstrap
	invalid.FRP.TrustedCAPEM = ""
	if err := runner.Run(context.Background(), invalid, initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}); err == nil || starts != 0 || stops != 1 {
		t.Fatalf("unsafe bootstrap reached engine: %v starts=%d stops=%d", err, starts, stops)
	}
	if err := runner.Run(context.Background(), bootstrap, initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}); !errors.Is(err, ErrEngineStopped) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("engine error leaked: %v", err)
	}
}
