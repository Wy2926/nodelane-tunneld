package controlserver

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
)

const recoveryRunID = "run_aaaaaaaaaaaaaaaaaaaaaaaaaa"
const recoveryProxy = "rte_aaaaaaaaaaaaaaaaaaaaaaaaaa"

type recoveryObserver struct {
	calls  *[]string
	client frpevidence.ClientEvidence
	proxy  frpevidence.Evidence
}

func (o recoveryObserver) ObserveClient(context.Context, string) frpevidence.ClientEvidence {
	*o.calls = append(*o.calls, "client")
	return o.client
}
func (o recoveryObserver) Observe(context.Context, frpevidence.Expected) frpevidence.Evidence {
	*o.calls = append(*o.calls, "proxy")
	return o.proxy
}

func TestEntryRecoveryRequiresCurrentClientThenProxyEvidence(t *testing.T) {
	expected := frpevidence.Expected{RunID: recoveryRunID, ProxyName: recoveryProxy, Protocol: "http"}
	for _, test := range []struct {
		name   string
		client frpevidence.ClientEvidence
		proxy  frpevidence.Evidence
		want   entryRelease
	}{
		{name: "no client", client: frpevidence.ClientEvidence{Availability: frpevidence.NotObserved}, want: entryHeld},
		{name: "client unavailable", client: frpevidence.ClientEvidence{Availability: frpevidence.Unavailable}, want: entryHeld},
		{name: "client online", client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: recoveryRunID, Online: true}, want: entryHeld},
		{name: "different client", client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: "other", DisconnectedAt: 1}, want: entryHeld},
		{name: "offline proxy", client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: recoveryRunID, DisconnectedAt: 1}, proxy: frpevidence.Evidence{Availability: frpevidence.Available, RunID: recoveryRunID, ProxyName: recoveryProxy, Protocol: "http", Phase: "offline"}, want: entryOffline},
		{name: "absent after client drained", client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: recoveryRunID, DisconnectedAt: 1}, proxy: frpevidence.Evidence{Availability: frpevidence.NotObserved}, want: entryAbsent},
		{name: "missing proxy sample", client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: recoveryRunID, DisconnectedAt: 1}, proxy: frpevidence.Evidence{Availability: frpevidence.Unavailable}, want: entryHeld},
		{name: "connections remain", client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: recoveryRunID, DisconnectedAt: 1}, proxy: frpevidence.Evidence{Availability: frpevidence.Available, RunID: recoveryRunID, ProxyName: recoveryProxy, Protocol: "http", Phase: "offline", CurrentConnections: 1}, want: entryHeld},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			observer := recoveryObserver{&calls, test.client, test.proxy}
			if got := observeReleasedEntry(context.Background(), observer, expected); got != test.want {
				t.Fatalf("result=%v want=%v", got, test.want)
			}
			if len(calls) < 1 || calls[0] != "client" {
				t.Fatalf("order=%v", calls)
			}
			if test.client.Availability != frpevidence.Available || test.client.ClientID != recoveryRunID || test.client.Online {
				if len(calls) != 1 {
					t.Fatalf("untrusted client fetched proxy: %v", calls)
				}
			}
		})
	}
}

type registeredRecoveryProbe struct {
	calls      *[]string
	granted    bool
	confirm    domain.RunDisconnectEvidence
	releaseErr error
}

func (p *registeredRecoveryProbe) PendingRunReconciliation(context.Context, int) ([]domain.RunAuthorization, error) {
	*p.calls = append(*p.calls, "claim")
	return []domain.RunAuthorization{{Run: domain.Run{ID: recoveryRunID, RouteID: recoveryProxy, ProxyRegistrationGranted: p.granted}, Route: domain.Route{ID: recoveryProxy, ProxyName: recoveryProxy, Protocol: "http"}}}, nil
}
func (p *registeredRecoveryProbe) ReleaseNeverGranted(context.Context, string) (domain.Run, error) {
	*p.calls = append(*p.calls, "never-granted")
	return domain.Run{}, p.releaseErr
}
func (p *registeredRecoveryProbe) ConfirmOffline(_ context.Context, e domain.RunDisconnectEvidence) (domain.Run, error) {
	*p.calls = append(*p.calls, "confirm")
	p.confirm = e
	return domain.Run{}, p.releaseErr
}

func TestRegisteredRecoveryUsesAuthorityForNeverGrantedWithoutNativeGuess(t *testing.T) {
	calls := []string{}
	store := &registeredRecoveryProbe{calls: &calls}
	worker := registeredReconciler{store: store, observer: recoveryObserver{calls: &calls}}
	if err := worker.reconcile(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"claim", "never-granted"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRegisteredRecoveryAbsentEvidenceIsExplicitAndFailureIsSanitized(t *testing.T) {
	calls := []string{}
	store := &registeredRecoveryProbe{calls: &calls, granted: true, releaseErr: errors.New("private data")}
	observer := recoveryObserver{calls: &calls, client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: recoveryRunID, DisconnectedAt: 1}, proxy: frpevidence.Evidence{Availability: frpevidence.NotObserved}}
	err := (&registeredReconciler{store: store, observer: observer}).reconcile(context.Background(), 1)
	if !errors.Is(err, errRecoveryUnavailable) || !store.confirm.ProxyNotObserved || !store.confirm.ConfirmedClientDisconnected || store.confirm.ObservedOffline {
		t.Fatalf("evidence=%v err=%v", store.confirm, err)
	}
}

type anonymousRecoveryProbe struct {
	calls    *[]string
	evidence anonymous.ReleaseEvidence
	granted  bool
}

func (p *anonymousRecoveryProbe) PendingVerification(context.Context, int64) ([]anonymous.VerificationItem, error) {
	*p.calls = append(*p.calls, "claim")
	return []anonymous.VerificationItem{{RunID: "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa", ProxyName: "anon_aaaaaaaaaaaaaaaaaaaaaaaaaa", Protocol: anonymous.ProtocolHTTP, ProxyRegistrationGranted: p.granted}}, nil
}
func (p *anonymousRecoveryProbe) ReleaseNeverGranted(context.Context, string) error {
	*p.calls = append(*p.calls, "never-granted")
	return nil
}
func (p *anonymousRecoveryProbe) ConfirmReleased(_ context.Context, e anonymous.ReleaseEvidence) error {
	*p.calls = append(*p.calls, "confirm")
	p.evidence = e
	return nil
}

func TestAnonymousRecoveryUsesSameEvidenceContractAndBoundsClaims(t *testing.T) {
	calls := []string{}
	store := &anonymousRecoveryProbe{calls: &calls, granted: true}
	observer := recoveryObserver{calls: &calls, client: frpevidence.ClientEvidence{Availability: frpevidence.Available, ClientID: "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa", DisconnectedAt: 1}, proxy: frpevidence.Evidence{Availability: frpevidence.NotObserved}}
	if err := (&anonymousReconciler{store: store, observer: observer}).reconcile(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if store.evidence.Kind != anonymous.ReleaseEvidenceDrainedAbsentProxy || !store.evidence.ConfirmedClientDisconnected || !store.evidence.ProxyNotObserved {
		t.Fatalf("evidence=%v", store.evidence)
	}
	if len(calls) != 5 {
		t.Fatalf("duplicate claim processed twice: %v", calls)
	}
}
