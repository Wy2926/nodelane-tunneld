package runclient

import (
	"context"
	"errors"
	"io"
	rand "math/rand/v2"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	legacyclient "github.com/Wy2926/nodelane-tunneld/internal/client"
	"github.com/Wy2926/nodelane-tunneld/internal/runsecret"
)

var (
	ErrConnectDeadline  = errors.New("connect_deadline_exceeded")
	ErrLeaseExpired     = errors.New("lease_expired")
	ErrHardExpired      = errors.New("hard_expired")
	ErrRunStopped       = errors.New("run_stopped")
	ErrEngineStopped    = errors.New("engine_stopped")
	ErrCloseUnconfirmed = errors.New("close_unconfirmed")
	ErrProxyUnsupported = errors.New("frp_proxy_unsupported")
)

type StatusState string

const (
	StatusConnecting   StatusState = "connecting"
	StatusOnline       StatusState = "online"
	StatusReconnecting StatusState = "reconnecting"
	StatusStopping     StatusState = "stopping"
	StatusStopped      StatusState = "stopped"
)

type Status struct {
	State          StatusState `json:"state"`
	RunID          string      `json:"run_id"`
	PublicEndpoint string      `json:"public_endpoint,omitempty"`
	Code           string      `json:"code,omitempty"`
}

type RunBackend interface {
	Heartbeat(context.Context, string, string) (HeartbeatResult, error)
	Stop(context.Context, string, string) (Run, error)
}

type Engine interface {
	Run(context.Context) error
	Close()
}

type RunnerOptions struct {
	Backend           RunBackend
	CAFile            string
	ProxyURL          string
	OnStatus          func(Status)
	HeartbeatInterval time.Duration
	StopTimeout       time.Duration
	CloseTimeout      time.Duration
	EngineFactory     func(string) (Engine, error)
	Now               func() time.Time
	Wait              func(context.Context, time.Duration) error
	Jitter            func(time.Duration) time.Duration
}

type Runner struct{ options RunnerOptions }

func NewRunner(options RunnerOptions) (*Runner, error) {
	if err := ValidateProxyURL(options.ProxyURL); err != nil {
		return nil, err
	}
	if nilInterface(options.Backend) {
		return nil, ErrInvalidConfiguration
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = 5 * time.Second
	}
	if options.StopTimeout == 0 {
		options.StopTimeout = 2 * time.Second
	}
	if options.CloseTimeout == 0 {
		options.CloseTimeout = 2 * time.Second
	}
	if options.HeartbeatInterval <= 0 || options.HeartbeatInterval > 5*time.Second || options.StopTimeout <= 0 || options.StopTimeout > 5*time.Second || options.CloseTimeout <= 0 || options.CloseTimeout > 5*time.Second {
		return nil, ErrInvalidConfiguration
	}
	if options.EngineFactory == nil {
		options.EngineFactory = func(text string) (Engine, error) { return legacyclient.NewEmbeddedFRPClient(text, io.Discard) }
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Wait == nil {
		options.Wait = waitContext
	}
	if options.Jitter == nil {
		options.Jitter = func(d time.Duration) time.Duration { return time.Duration(float64(d) * (0.9 + rand.Float64()*0.2)) }
	}
	return &Runner{options: options}, nil
}

func (r *Runner) Run(ctx context.Context, bootstrap BootstrapConfig, run Run, target Target) (resultErr error) {
	if !validAllocatedRun(run) || !validTarget(target) || run.Protocol != target.Protocol {
		return ErrInvalidRequest
	}
	var engine Engine
	var engineDone <-chan struct{}
	var cancelEngine context.CancelFunc
	var credentialSource *runsecret.Source
	var temporaryCAPath string
	var lastState StatusState
	emit := func(state StatusState, code string) {
		if state == lastState && code == "" {
			return
		}
		lastState = state
		if r.options.OnStatus != nil {
			r.options.OnStatus(Status{State: state, RunID: run.ID, PublicEndpoint: run.PublicEndpoint, Code: code})
		}
	}
	defer func() {
		if temporaryCAPath != "" {
			defer os.Remove(temporaryCAPath)
		}
		if cancelEngine != nil {
			cancelEngine()
		}
		if engine != nil {
			engine.Close()
		}
		emit(StatusStopping, "")
		stopDone := make(chan error, 1)
		stopCtx, cancelStop := context.WithTimeout(context.Background(), r.options.StopTimeout)
		defer cancelStop()
		go func() { _, err := r.options.Backend.Stop(stopCtx, run.ID, run.CredentialToken); stopDone <- err }()
		closeCtx, cancelClose := context.WithTimeout(context.Background(), r.options.CloseTimeout)
		defer cancelClose()
		closeConfirmed := true
		if engineDone != nil {
			select {
			case <-engineDone:
			case <-closeCtx.Done():
				select {
				case <-engineDone:
				default:
					closeConfirmed = false
				}
			}
		}
		if credentialSource != nil {
			if err := credentialSource.Close(); err != nil {
				closeConfirmed = false
			}
		}
		code := ""
		select {
		case err := <-stopDone:
			if err != nil {
				code = "stop_report_failed"
			}
		case <-stopCtx.Done():
			code = "stop_report_failed"
		}
		if !closeConfirmed {
			resultErr = ErrCloseUnconfirmed
			emit(StatusStopping, "close_unconfirmed")
			return
		}
		emit(StatusStopped, code)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := newRunDeadlines(run, r.options.Now())
	if err := deadline.expired(r.options.Now()); err != nil {
		return err
	}
	if err := ValidateBootstrapConfig(bootstrap, r.options.CAFile); err != nil {
		return err
	}
	ca, err := trustedCA(bootstrap, r.options.CAFile)
	if err != nil {
		return err
	}
	caFile, err := os.CreateTemp("", "nt-frp-ca-*.pem")
	if err != nil {
		return ErrInvalidConfiguration
	}
	temporaryCAPath = caFile.Name()
	if _, err := caFile.Write(ca); err != nil {
		_ = caFile.Close()
		return ErrInvalidConfiguration
	}
	if err := caFile.Close(); err != nil {
		return ErrInvalidConfiguration
	}
	// Upstream file reads do not honor the run context. Keep this owned memory
	// source alive until engine shutdown, then revoke it even if shutdown failed.
	credentialSource, err = runsecret.New(context.Background(), run.CredentialToken)
	if err != nil {
		return ErrEngineStopped
	}
	configText, err := buildFRPConfig(bootstrap, run, target, caFile.Name(), credentialSource.Path(), r.options.ProxyURL)
	if err != nil {
		return err
	}
	engine, err = r.options.EngineFactory(configText)
	if err != nil || nilInterface(engine) {
		engine = nil
		return ErrEngineStopped
	}
	engineCtx, cancel := context.WithCancel(ctx)
	cancelEngine = cancel
	done := make(chan struct{})
	engineDone = done
	go func() { _ = engine.Run(engineCtx); close(done) }()
	emit(StatusConnecting, "")
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := r.options.Now()
		if err := deadline.expired(now); err != nil {
			return err
		}
		delay := r.options.Jitter(r.options.HeartbeatInterval)
		if delay < r.options.HeartbeatInterval/2 {
			delay = r.options.HeartbeatInterval / 2
		}
		if delay > r.options.HeartbeatInterval*3/2 {
			delay = r.options.HeartbeatInterval * 3 / 2
		}
		if left := deadline.next().Sub(now); delay > left {
			delay = left
		}
		if err := r.wait(ctx, engineDone, delay); err != nil {
			return err
		}
		now = r.options.Now()
		if err := deadline.expired(now); err != nil {
			return err
		}
		heartbeat, err := r.heartbeat(ctx, engineDone, run, deadline.next().Sub(now))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrEngineStopped) {
				return err
			}
			var apiError *APIError
			if errors.As(err, &apiError) && (apiError.StatusCode == http.StatusUnauthorized || apiError.StatusCode == http.StatusGone) {
				return ErrRunStopped
			}
			emit(StatusReconnecting, safeHeartbeatCode(err))
			continue
		}
		if heartbeat.Stopped || heartbeat.Run.DesiredState == "stopped" {
			return ErrRunStopped
		}
		if heartbeat.Run.ID != run.ID || heartbeat.Run.DesiredState != "running" {
			emit(StatusReconnecting, "invalid_response")
			continue
		}
		now = r.options.Now()
		if err := deadline.expired(now); err != nil {
			return err
		}
		deadline.update(heartbeat.Run, now)
		if deadline.connected {
			emit(StatusOnline, "")
		}
	}
}

func (r *Runner) wait(ctx context.Context, engineDone <-chan struct{}, duration time.Duration) error {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.options.Wait(waitCtx, duration) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-engineDone:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrEngineStopped
	case err := <-done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return ErrEngineStopped
		}
		return nil
	}
}

func (r *Runner) heartbeat(ctx context.Context, engineDone <-chan struct{}, run Run, remaining time.Duration) (HeartbeatResult, error) {
	if remaining > requestTimeout {
		remaining = requestTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	type answer struct {
		result HeartbeatResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := r.options.Backend.Heartbeat(requestCtx, run.ID, run.CredentialToken)
		done <- answer{result, err}
	}()
	select {
	case <-ctx.Done():
		return HeartbeatResult{}, ctx.Err()
	case <-engineDone:
		if ctx.Err() != nil {
			return HeartbeatResult{}, ctx.Err()
		}
		return HeartbeatResult{}, ErrEngineStopped
	case <-requestCtx.Done():
		return HeartbeatResult{}, &APIError{Code: "request_timeout"}
	case response := <-done:
		return response.result, response.err
	}
}

type runDeadlines struct {
	connect   time.Time
	lease     time.Time
	hard      time.Time
	connected bool
}

func newRunDeadlines(run Run, now time.Time) runDeadlines {
	d := runDeadlines{connect: earlier(run.ConnectDeadlineAt, run.CreatedAt.Add(2*time.Minute))}
	if strings.HasPrefix(run.ID, "anr_") {
		d.hard = earlier(run.HardExpiresAt, run.CreatedAt.Add(time.Hour))
	}
	d.update(run, now)
	return d
}

func (d *runDeadlines) update(run Run, now time.Time) {
	if !run.HardExpiresAt.IsZero() {
		d.hard = earlier(d.hard, run.HardExpiresAt)
	}
	if !run.LeaseExpiresAt.IsZero() {
		d.connected = true
		d.lease = earlier(run.LeaseExpiresAt, now.Add(90*time.Second))
	}
}

func (d runDeadlines) next() time.Time {
	deadline := d.connect
	if d.connected {
		deadline = d.lease
	}
	return earlier(deadline, d.hard)
}

func (d runDeadlines) expired(now time.Time) error {
	if !d.hard.IsZero() && !now.Before(d.hard) {
		return ErrHardExpired
	}
	if d.connected {
		if !now.Before(d.lease) {
			return ErrLeaseExpired
		}
	} else if !now.Before(d.connect) {
		return ErrConnectDeadline
	}
	return nil
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() || a.Before(b) {
		return a
	}
	return b
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func safeHeartbeatCode(err error) string {
	var apiError *APIError
	if errors.As(err, &apiError) {
		return safeErrorCode(apiError.Code)
	}
	return "network_unavailable"
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	typed := reflect.ValueOf(value)
	switch typed.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return typed.IsNil()
	}
	return false
}
