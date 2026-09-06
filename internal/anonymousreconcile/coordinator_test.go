package anonymousreconcile_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/anonymousreconcile"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
)

const (
	runA   = "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	runB   = "anr_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	proxyA = "anon_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	proxyB = "anon_bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestObserveConnectedRequiresFreshExactOnlineEvidence(t *testing.T) {
	tests := []struct {
		name     string
		evidence frpevidence.Evidence
		wantMark bool
	}{
		{name: "online zero", evidence: sample("online", 0), wantMark: true},
		{name: "online positive", evidence: sample("online", 3), wantMark: true},
		{name: "offline", evidence: sample("offline", 0)},
		{name: "not observed", evidence: frpevidence.Evidence{Availability: frpevidence.NotObserved}},
		{name: "unavailable", evidence: frpevidence.Evidence{Availability: frpevidence.Unavailable}},
		{name: "negative connections", evidence: sample("online", -1)},
		{name: "wrong run", evidence: frpevidence.Evidence{Availability: frpevidence.Available, RunID: runB, ProxyName: proxyA, Protocol: "http", Phase: "online"}},
		{name: "wrong proxy", evidence: frpevidence.Evidence{Availability: frpevidence.Available, RunID: runA, ProxyName: proxyB, Protocol: "http", Phase: "online"}},
		{name: "wrong protocol", evidence: frpevidence.Evidence{Availability: frpevidence.Available, RunID: runA, ProxyName: proxyA, Protocol: "tcp", Phase: "online"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{}
			observer := &recordingObserver{evidence: test.evidence}
			coordinator := newCoordinator(t, store, observer, nil)
			got, err := coordinator.ObserveConnected(context.Background(), expected())
			if test.wantMark {
				if err != nil || got.RunID != runA || got.State != anonymous.StateOnline || !reflect.DeepEqual(store.marked, []frpevidence.Expected{expected()}) {
					t.Fatalf("online observation = %#v, %v; marked=%#v", got, err, store.marked)
				}
			} else if !errors.Is(err, anonymousreconcile.ErrObservationUnconfirmed) || got != (anonymous.Run{}) || len(store.marked) != 0 {
				t.Fatalf("unconfirmed observation changed state: %#v, %v; marked=%#v", got, err, store.marked)
			}
			if !reflect.DeepEqual(observer.expected, []frpevidence.Expected{expected()}) || len(store.released) != 0 || store.pendingCalls != 0 {
				t.Fatalf("unexpected observation dependencies: %#v %#v", observer, store)
			}
		})
	}
}

func TestObserveConnectedRejectsInvalidAnonymousExpectationBeforeDependencies(t *testing.T) {
	for _, input := range []frpevidence.Expected{
		{},
		{RunID: runA, ProxyName: proxyA, Protocol: "https"},
		{RunID: "run_aaaaaaaaaaaaaaaaaaaaaaaaaa", ProxyName: "rte_aaaaaaaaaaaaaaaaaaaaaaaaaa", Protocol: "http"},
		{RunID: runA, ProxyName: proxyA + "/traffic", Protocol: "http"},
		{RunID: "anr_00000000000000000000000000", ProxyName: proxyA, Protocol: "http"},
	} {
		store := &recordingStore{}
		observer := &recordingObserver{evidence: sample("online", 0)}
		_, err := newCoordinator(t, store, observer, nil).ObserveConnected(context.Background(), input)
		if !errors.Is(err, anonymousreconcile.ErrInvalidRequest) || len(observer.expected) != 0 || len(store.marked) != 0 {
			t.Fatalf("invalid expectation reached dependencies: %#v, %v", input, err)
		}
	}
}

func TestObserveConnectedPreservesStoreExpiryRejectionWithoutSecrets(t *testing.T) {
	store := &recordingStore{markErr: errors.Join(anonymous.ErrRunStopped, errors.New("secret-token"))}
	coordinator := newCoordinator(t, store, &recordingObserver{evidence: sample("online", 0)}, nil)
	run, err := coordinator.ObserveConnected(context.Background(), expected())
	if !errors.Is(err, anonymous.ErrRunStopped) || strings.Contains(err.Error(), "secret-token") || run != (anonymous.Run{}) {
		t.Fatalf("expired observation error = %#v, %v", run, err)
	}
}

func TestReconcileRequiresDrainProofThenFreshOfflineZeroSample(t *testing.T) {
	tests := []struct {
		name     string
		evidence frpevidence.Evidence
		guard    bool
		guardErr error
		missing  bool
		released int
		failed   int
	}{
		{name: "trusted offline zero", evidence: sample("offline", 0), guard: true, released: 1},
		{name: "no drain proof", evidence: sample("offline", 0)},
		{name: "missing guard", evidence: sample("offline", 0), missing: true},
		{name: "guard unavailable", evidence: sample("offline", 0), guard: true, guardErr: errors.New("secret drain failure"), failed: 1},
		{name: "online zero", evidence: sample("online", 0), guard: true},
		{name: "offline positive", evidence: sample("offline", 1), guard: true},
		{name: "offline negative", evidence: sample("offline", -1), guard: true},
		{name: "unknown 404", evidence: frpevidence.Evidence{Availability: frpevidence.NotObserved}, guard: true},
		{name: "unavailable", evidence: frpevidence.Evidence{Availability: frpevidence.Unavailable}, guard: true},
		{name: "wrong run", evidence: frpevidence.Evidence{Availability: frpevidence.Available, RunID: runB, ProxyName: proxyA, Protocol: "http", Phase: "offline"}, guard: true},
		{name: "wrong proxy", evidence: frpevidence.Evidence{Availability: frpevidence.Available, RunID: runA, ProxyName: proxyB, Protocol: "http", Phase: "offline"}, guard: true},
		{name: "wrong protocol", evidence: frpevidence.Evidence{Availability: frpevidence.Available, RunID: runA, ProxyName: proxyA, Protocol: "tcp", Phase: "offline"}, guard: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			store := &recordingStore{items: []anonymous.VerificationItem{item()}, calls: &calls}
			observer := &recordingObserver{evidence: test.evidence, calls: &calls}
			guard := &recordingGuard{confirmed: test.guard, err: test.guardErr, calls: &calls}
			var releaseGuard anonymousreconcile.ReleaseGuard = guard
			if test.missing {
				releaseGuard = nil
			}
			report, err := newCoordinator(t, store, observer, releaseGuard).Reconcile(context.Background(), 10)
			want := anonymousreconcile.Report{Inspected: 1, Released: test.released, Held: 1 - test.released, Failed: test.failed}
			if report != want || (err != nil) != (test.failed != 0) || err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatalf("report = %#v, %v; want %#v", report, err, want)
			}
			if len(store.released) != test.released || store.pendingLimit != 10 || len(store.marked) != 0 {
				t.Fatalf("unexpected reconciliation mutations: %#v", store)
			}
			if test.released == 1 {
				wantEvidence := anonymous.ReleaseEvidence{
					Kind: anonymous.ReleaseEvidenceOfflineSample, RunID: runA, ProxyName: proxyA,
					ObservedOffline: true, SampleAvailable: true, CurrentConnections: 0,
				}
				if !reflect.DeepEqual(store.released, []anonymous.ReleaseEvidence{wantEvidence}) || !reflect.DeepEqual(calls, []string{"pending", "guard", "observe", "release"}) {
					t.Fatalf("release did not follow proof and fresh observation: %#v, %v", store.released, calls)
				}
			}
			if (test.missing || !test.guard || test.guardErr != nil) && len(observer.expected) != 0 {
				t.Fatal("used a sample without a preceding trusted drain proof")
			}
			if !test.missing && !reflect.DeepEqual(guard.items, []anonymous.VerificationItem{item()}) {
				t.Fatalf("guard received wrong run: %#v", guard.items)
			}
		})
	}
}

func TestReconcileProcessesValidItemsAlongsideCorruptionAndSanitizesStoreErrors(t *testing.T) {
	store := &recordingStore{items: []anonymous.VerificationItem{item()}, pendingErr: errors.Join(&anonymous.VerificationCorruptionError{Count: 1}, errors.New("secret-key"))}
	coordinator := newCoordinator(t, store, &recordingObserver{evidence: sample("offline", 0)}, &recordingGuard{confirmed: true})
	report, err := coordinator.Reconcile(context.Background(), 10)
	if report != (anonymousreconcile.Report{Inspected: 1, Released: 1}) || !errors.Is(err, anonymous.ErrVerificationCorrupt) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("valid work was lost or error leaked: %#v, %v", report, err)
	}

	store.pendingErr = errors.New("secret redis endpoint")
	store.released = nil
	report, err = coordinator.Reconcile(context.Background(), 10)
	if report != (anonymousreconcile.Report{}) || !errors.Is(err, anonymousreconcile.ErrStoreUnavailable) || strings.Contains(err.Error(), "secret") || len(store.released) != 0 {
		t.Fatalf("failed queue supplied work or leaked error: %#v, %v", report, err)
	}
}

func TestReconcileKeepsResourceHeldWhenFinalStoreCompareRejects(t *testing.T) {
	store := &recordingStore{items: []anonymous.VerificationItem{item()}, releaseErr: errors.Join(anonymous.ErrInvalidState, errors.New("secret owner"))}
	report, err := newCoordinator(t, store, &recordingObserver{evidence: sample("offline", 0)}, &recordingGuard{confirmed: true}).Reconcile(context.Background(), 10)
	if report != (anonymousreconcile.Report{Inspected: 1, Held: 1, Failed: 1}) || !errors.Is(err, anonymous.ErrInvalidState) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("failed compare was reported as released: %#v, %v", report, err)
	}
}

func TestReconcileRejectsUnboundedOrInvalidWork(t *testing.T) {
	for _, limit := range []int64{-1, 0, 1001} {
		store := &recordingStore{}
		_, err := newCoordinator(t, store, &recordingObserver{}, nil).Reconcile(context.Background(), limit)
		if !errors.Is(err, anonymousreconcile.ErrInvalidRequest) || store.pendingCalls != 0 {
			t.Fatalf("invalid limit %d reached store: %v", limit, err)
		}
	}
	store := &recordingStore{items: []anonymous.VerificationItem{item(), item()}}
	observer := &recordingObserver{evidence: sample("offline", 0)}
	_, err := newCoordinator(t, store, observer, &recordingGuard{confirmed: true}).Reconcile(context.Background(), 1)
	if !errors.Is(err, anonymous.ErrInvalidState) || len(observer.expected) != 0 || len(store.released) != 0 {
		t.Fatalf("oversized queue result was consumed: %v", err)
	}
	store.items = []anonymous.VerificationItem{{RunID: runA, ProxyName: proxyA, Protocol: "https"}}
	report, err := newCoordinator(t, store, observer, &recordingGuard{confirmed: true}).Reconcile(context.Background(), 1)
	if report != (anonymousreconcile.Report{Inspected: 1, Held: 1, Failed: 1}) || !errors.Is(err, anonymous.ErrInvalidState) || len(observer.expected) != 0 || len(store.released) != 0 {
		t.Fatalf("invalid queue identity reached dependencies: %#v, %v", report, err)
	}
}

func TestCancellationStopsEveryLaterDependencyCall(t *testing.T) {
	for _, stage := range []string{"before", "pending", "guard", "observe", "release"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []string
			store := &recordingStore{items: []anonymous.VerificationItem{item(), item()}, calls: &calls}
			observer := &recordingObserver{evidence: sample("offline", 0), calls: &calls}
			guard := &recordingGuard{confirmed: true, calls: &calls}
			switch stage {
			case "before":
				cancel()
			case "pending":
				store.afterPending = cancel
			case "guard":
				guard.after = cancel
			case "observe":
				observer.after = cancel
			case "release":
				store.afterRelease = cancel
			}
			_, err := newCoordinator(t, store, observer, guard).Reconcile(ctx, 2)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v", err)
			}
			wantCalls := map[string][]string{
				"before": nil, "pending": {"pending"}, "guard": {"pending", "guard"},
				"observe": {"pending", "guard", "observe"}, "release": {"pending", "guard", "observe", "release"},
			}[stage]
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("calls after cancellation = %v, want %v", calls, wantCalls)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	store := &recordingStore{}
	observer := &recordingObserver{evidence: sample("online", 0), after: cancel}
	_, err := newCoordinator(t, store, observer, nil).ObserveConnected(ctx, expected())
	if !errors.Is(err, context.Canceled) || len(store.marked) != 0 {
		t.Fatalf("canceled observation marked connected: %v", err)
	}
}

func TestNewRejectsNilDependenciesAndTypedNilGuardHolds(t *testing.T) {
	var store *recordingStore
	var observer *recordingObserver
	for _, dependencies := range []struct {
		store    anonymousreconcile.Store
		observer anonymousreconcile.Observer
	}{{nil, &recordingObserver{}}, {store, &recordingObserver{}}, {&recordingStore{}, nil}, {&recordingStore{}, observer}} {
		if _, err := anonymousreconcile.New(dependencies.store, dependencies.observer, nil); !errors.Is(err, anonymousreconcile.ErrInvalidConfiguration) {
			t.Fatalf("nil dependency accepted: %v", err)
		}
	}
	var guard *recordingGuard
	validStore := &recordingStore{items: []anonymous.VerificationItem{item()}}
	report, err := newCoordinator(t, validStore, &recordingObserver{evidence: sample("offline", 0)}, guard).Reconcile(context.Background(), 1)
	if err != nil || report != (anonymousreconcile.Report{Inspected: 1, Held: 1}) || len(validStore.released) != 0 {
		t.Fatalf("typed nil guard did not hold: %#v, %v", report, err)
	}
}

type recordingStore struct {
	items        []anonymous.VerificationItem
	pendingErr   error
	markErr      error
	releaseErr   error
	pendingCalls int
	pendingLimit int64
	marked       []frpevidence.Expected
	released     []anonymous.ReleaseEvidence
	calls        *[]string
	afterPending func()
	afterRelease func()
}

func (s *recordingStore) PendingVerification(_ context.Context, limit int64) ([]anonymous.VerificationItem, error) {
	s.pendingCalls++
	s.pendingLimit = limit
	appendCall(s.calls, "pending")
	if s.afterPending != nil {
		s.afterPending()
	}
	return s.items, s.pendingErr
}

func (s *recordingStore) MarkConnected(_ context.Context, runID, proxyName string) (anonymous.Run, error) {
	s.marked = append(s.marked, frpevidence.Expected{RunID: runID, ProxyName: proxyName, Protocol: "http"})
	if s.markErr != nil {
		return anonymous.Run{}, s.markErr
	}
	return anonymous.Run{RunID: runID, ProxyName: proxyName, Protocol: anonymous.ProtocolHTTP, State: anonymous.StateOnline, DesiredState: anonymous.DesiredRunning}, nil
}

func (s *recordingStore) ConfirmReleased(_ context.Context, evidence anonymous.ReleaseEvidence) error {
	s.released = append(s.released, evidence)
	appendCall(s.calls, "release")
	if s.afterRelease != nil {
		s.afterRelease()
	}
	return s.releaseErr
}

type recordingObserver struct {
	evidence frpevidence.Evidence
	expected []frpevidence.Expected
	calls    *[]string
	after    func()
}

func (o *recordingObserver) Observe(_ context.Context, expected frpevidence.Expected) frpevidence.Evidence {
	o.expected = append(o.expected, expected)
	appendCall(o.calls, "observe")
	if o.after != nil {
		o.after()
	}
	return o.evidence
}

type recordingGuard struct {
	confirmed bool
	err       error
	items     []anonymous.VerificationItem
	calls     *[]string
	after     func()
}

func (g *recordingGuard) CanConfirmRelease(_ context.Context, item anonymous.VerificationItem) (bool, error) {
	g.items = append(g.items, item)
	appendCall(g.calls, "guard")
	if g.after != nil {
		g.after()
	}
	return g.confirmed, g.err
}

func appendCall(calls *[]string, value string) {
	if calls != nil {
		*calls = append(*calls, value)
	}
}

func expected() frpevidence.Expected {
	return frpevidence.Expected{RunID: runA, ProxyName: proxyA, Protocol: "http"}
}

func item() anonymous.VerificationItem {
	return anonymous.VerificationItem{RunID: runA, ProxyName: proxyA, Protocol: anonymous.ProtocolHTTP, PublicEndpoint: "anon-aaaaaaaaaaaaaaaaaaaaaaaaaa.tunnel.test"}
}

func sample(phase string, connections int64) frpevidence.Evidence {
	return frpevidence.Evidence{Availability: frpevidence.Available, RunID: runA, ProxyName: proxyA, Protocol: "http", Phase: phase, CurrentConnections: connections}
}

func newCoordinator(t *testing.T, store anonymousreconcile.Store, observer anonymousreconcile.Observer, guard anonymousreconcile.ReleaseGuard) *anonymousreconcile.Coordinator {
	t.Helper()
	coordinator, err := anonymousreconcile.New(store, observer, guard)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}
