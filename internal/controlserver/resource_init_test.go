package controlserver

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
)

type freshResourceProbe struct {
	calls   *[]string
	fence   anonymous.ResourceFence
	invalid bool
}

func (p freshResourceProbe) PrepareFreshInitialization(context.Context) (anonymous.ResourceFence, error) {
	*p.calls = append(*p.calls, "prepare")
	return p.fence, nil
}
func (p freshResourceProbe) AssertFreshNamespace(_ context.Context, f anonymous.ResourceFence) error {
	*p.calls = append(*p.calls, "fresh")
	if f != p.fence || p.invalid {
		return errors.New("not fresh")
	}
	return nil
}
func (p freshResourceProbe) MarkResourcesVerified(_ context.Context, f anonymous.ResourceFence) (anonymous.ResourceFence, error) {
	*p.calls = append(*p.calls, "mark")
	if f != p.fence {
		return anonymous.ResourceFence{}, errors.New("wrong fence")
	}
	return f, nil
}

type inventoryProbe struct {
	calls  *[]string
	result frpevidence.Inventory
}

func (p inventoryProbe) ListAnonymous(context.Context) frpevidence.Inventory {
	*p.calls = append(*p.calls, "inventory")
	return p.result
}

func TestResourceInitializationRequiresExplicitMaintenanceConfirmation(t *testing.T) {
	calls := []string{}
	err := initializeFreshResources(context.Background(), false, freshResourceProbe{calls: &calls}, inventoryProbe{calls: &calls})
	if !errors.Is(err, errMaintenanceConfirmation) || len(calls) != 0 {
		t.Fatalf("initialization without permission=%v calls=%v", err, calls)
	}
}

func TestResourceInitializationUsesExactObservedFenceAndRefusesUnknownData(t *testing.T) {
	for _, test := range []struct {
		name      string
		invalid   bool
		inventory frpevidence.Inventory
		marked    bool
	}{
		{name: "empty", inventory: frpevidence.Inventory{Availability: frpevidence.Available, Proxies: []frpevidence.Evidence{}}, marked: true},
		{name: "existing state", invalid: true, inventory: frpevidence.Inventory{Availability: frpevidence.Available}},
		{name: "unavailable", inventory: frpevidence.Inventory{Availability: frpevidence.Unavailable}},
		{name: "old proxy", inventory: frpevidence.Inventory{Availability: frpevidence.Available, Proxies: []frpevidence.Evidence{{Phase: "offline"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			fence := anonymous.ResourceFence{RedisRunID: "fixture-process", Generation: 7}
			err := initializeFreshResources(context.Background(), true, freshResourceProbe{&calls, fence, test.invalid}, inventoryProbe{&calls, test.inventory})
			if (err == nil) != test.marked {
				t.Fatalf("err=%v marked=%v", err, test.marked)
			}
			want := []string{"prepare", "fresh"}
			if !test.invalid {
				want = append(want, "inventory")
			}
			if test.marked {
				want = append(want, "mark")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("order=%v want=%v", calls, want)
			}
		})
	}
}
