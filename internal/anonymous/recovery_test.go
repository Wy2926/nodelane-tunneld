package anonymous

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestAnonymousProxyRegistrationAuthorizationRecordsKnownGrantOnly(t *testing.T) {
	t.Run("new allocation", func(t *testing.T) {
		f := newRedisFixture(t, nil)
		f.ready(t)
		ctx := context.Background()
		allocation, err := f.store.Allocate(ctx, allocationRequest("proxy-registration-grant"))
		if err != nil {
			t.Fatal(err)
		}
		key := f.store.runKey(allocation.RunID)
		if value, err := f.client.HGet(ctx, key, "proxy_registration_granted").Result(); err != nil || value != "0" {
			t.Fatalf("new allocation evidence=%q err=%v", value, err)
		}
		ordinary, err := f.store.Authorize(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName)
		if err != nil || ordinary.ProxyRegistrationGranted {
			t.Fatalf("ordinary authorization=%+v err=%v", ordinary, err)
		}
		granted, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName)
		if err != nil || !granted.ProxyRegistrationGranted || !granted.LeaseExpiresAt.IsZero() {
			t.Fatalf("proxy registration authorization=%+v err=%v", granted, err)
		}
	})

	for _, stored := range []string{"missing", "malformed"} {
		t.Run(stored, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			allocation, err := f.store.Allocate(ctx, allocationRequest("proxy-registration-"+stored))
			if err != nil {
				t.Fatal(err)
			}
			key := f.store.runKey(allocation.RunID)
			if stored == "missing" {
				err = f.client.HDel(ctx, key, "proxy_registration_granted").Err()
			} else {
				err = f.client.HSet(ctx, key, "proxy_registration_granted", "2").Err()
			}
			if err != nil {
				t.Fatal(err)
			}
			if granted, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); !errors.Is(err, ErrInvalidState) || granted != (Run{}) {
				t.Fatalf("%s registration flag authorized=%+v err=%v", stored, granted, err)
			}
			if connected, err := f.store.MarkConnected(ctx, allocation.RunID, allocation.ProxyName); !errors.Is(err, ErrInvalidState) || connected != (Run{}) {
				t.Fatalf("%s registration flag connected=%+v err=%v", stored, connected, err)
			}
			if state, err := f.client.HGet(ctx, key, "state").Result(); err != nil || state != "reserved" {
				t.Fatalf("%s registration flag mutated state=%q err=%v", stored, state, err)
			}
		})
	}
}

func TestAnonymousTrustedConnectedMarksKnownGrantWithoutLaterLeaseRenewal(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	allocation, err := f.store.Allocate(ctx, allocationRequest("trusted-connected-grant"))
	if err != nil {
		t.Fatal(err)
	}
	connected, err := f.store.MarkConnected(ctx, allocation.RunID, allocation.ProxyName)
	if err != nil || !connected.ProxyRegistrationGranted {
		t.Fatalf("connected run=%+v err=%v", connected, err)
	}
	lease := connected.LeaseExpiresAt
	score, err := f.client.ZScore(ctx, f.store.verificationKey(), f.store.runKey(allocation.RunID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Set(f.clock.Now().Add(30 * time.Second))
	granted, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName)
	if err != nil {
		t.Fatal(err)
	}
	afterScore, err := f.client.ZScore(ctx, f.store.verificationKey(), f.store.runKey(allocation.RunID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if !granted.LeaseExpiresAt.Equal(lease) || afterScore != score {
		t.Fatalf("registration grant renewed lease/queue: lease=%v want=%v score=%v want=%v", granted.LeaseExpiresAt, lease, afterScore, score)
	}
}

func TestAnonymousReleaseNeverGrantedRequiresStoppedOrExpiredExplicitFalse(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		f := newRedisFixture(t, nil)
		f.ready(t)
		ctx := context.Background()
		allocation, err := f.store.Allocate(ctx, allocationRequest("release-never-granted-stopped"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.ReleaseNeverGranted(ctx, allocation.RunID); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("running run released: %v", err)
		}
		if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
			t.Fatal(err)
		}
		if err := f.store.ReleaseNeverGranted(ctx, allocation.RunID); err != nil {
			t.Fatal(err)
		}
		if err := f.store.ReleaseNeverGranted(ctx, allocation.RunID); err != nil {
			t.Fatalf("idempotent release: %v", err)
		}
		if _, err := f.store.Authorize(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); !errors.Is(err, ErrRunStopped) {
			t.Fatalf("released credential authorized: %v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		f := newRedisFixture(t, nil)
		f.ready(t)
		ctx := context.Background()
		allocation, err := f.store.Allocate(ctx, allocationRequest("release-never-granted-expired"))
		if err != nil {
			t.Fatal(err)
		}
		f.clock.Set(allocation.ConnectDeadlineAt)
		if err := f.store.ReleaseNeverGranted(ctx, allocation.RunID); err != nil {
			t.Fatalf("expired never-granted release: %v", err)
		}
	})

	for _, test := range []struct {
		name string
		want error
	}{
		{name: "missing", want: ErrInvalidState},
		{name: "granted", want: ErrReleaseUnconfirmed},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			allocation, err := f.store.Allocate(ctx, allocationRequest("release-refuses-"+test.name))
			if err != nil {
				t.Fatal(err)
			}
			key := f.store.runKey(allocation.RunID)
			if test.name == "granted" {
				if _, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
				t.Fatal(err)
			}
			if test.name == "missing" {
				if err := f.client.HDel(ctx, key, "proxy_registration_granted").Err(); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.ReleaseNeverGranted(ctx, allocation.RunID); !errors.Is(err, test.want) {
				t.Fatalf("%s evidence released: %v", test.name, err)
			}
			if owner, err := f.client.Get(ctx, f.store.proxyKey(allocation.ProxyName)).Result(); err != nil || owner != allocation.RunID {
				t.Fatalf("refused release changed owner=%q err=%v", owner, err)
			}
		})
	}
}

func TestAnonymousNeverGrantedReleaseValidatesCurrentOwnership(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	allocation, err := f.store.Allocate(ctx, allocationRequest("release-owner-check"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Set(ctx, f.store.proxyKey(allocation.ProxyName), "anr_aaaaaaaaaaaaaaaaaaaaaaaaaa", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReleaseNeverGranted(ctx, allocation.RunID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("mismatched ownership release=%v", err)
	}
	if _, err := f.client.Get(ctx, f.store.resourceKey(allocation.Protocol, allocation.PublicEndpoint)).Result(); err != nil {
		t.Fatalf("failed release deleted other ownership: %v", err)
	}
}

func TestAnonymousProxyGrantAndNeverGrantedReleaseCannotBothWin(t *testing.T) {
	f := newRedisFixture(t, nil)
	f.ready(t)
	ctx := context.Background()
	allocation, err := f.store.Allocate(ctx, allocationRequest("grant-release-race"))
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	grantDone := make(chan error, 1)
	releaseDone := make(chan error, 1)
	var ready sync.WaitGroup
	ready.Add(2)
	go func() {
		ready.Done()
		<-gate
		_, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName)
		grantDone <- err
	}()
	go func() {
		ready.Done()
		<-gate
		if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
			releaseDone <- err
			return
		}
		releaseDone <- f.store.ReleaseNeverGranted(ctx, allocation.RunID)
	}()
	ready.Wait()
	close(gate)
	grantErr, releaseErr := <-grantDone, <-releaseDone
	grantWon, releaseWon := grantErr == nil, releaseErr == nil
	if grantWon == releaseWon {
		t.Fatalf("grant/release winners: grant=%v release=%v", grantErr, releaseErr)
	}
	if grantWon && !errors.Is(releaseErr, ErrReleaseUnconfirmed) {
		t.Fatalf("grant winner produced release error=%v", releaseErr)
	}
	if releaseWon && !errors.Is(grantErr, ErrRunStopped) {
		t.Fatalf("release winner produced grant error=%v", grantErr)
	}
}

func TestAnonymousNeverRegisteredEvidenceRequiresValidStoredRegistrationFlag(t *testing.T) {
	for _, stored := range []string{"missing", "malformed"} {
		t.Run(stored, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			allocation, err := f.store.Allocate(ctx, allocationRequest(stored+"-never-registered"))
			if err != nil {
				t.Fatal(err)
			}
			if stored == "missing" {
				err = f.client.HDel(ctx, f.store.runKey(allocation.RunID), "proxy_registration_granted").Err()
			} else {
				err = f.client.HSet(ctx, f.store.runKey(allocation.RunID), "proxy_registration_granted", "2").Err()
			}
			if err != nil {
				t.Fatal(err)
			}
			f.clock.Set(allocation.ConnectDeadlineAt)
			evidence := ReleaseEvidence{Kind: ReleaseEvidenceNeverRegistered, RunID: allocation.RunID, ProxyName: allocation.ProxyName, ConfirmedNeverRegistered: true}
			if err := f.store.ConfirmReleased(ctx, evidence); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("%s registration flag accepted as never registered: %v", stored, err)
			}
			if _, err := f.client.Get(ctx, f.store.proxyKey(allocation.ProxyName)).Result(); errors.Is(err, redis.Nil) {
				t.Fatalf("%s registration flag released proxy ownership", stored)
			}
		})
	}
}

func TestAnonymousDrainedAbsentProxyRequiresKnownGrantAndDisconnectedClient(t *testing.T) {
	t.Run("authorized and drained", func(t *testing.T) {
		f := newRedisFixture(t, nil)
		f.ready(t)
		ctx := context.Background()
		allocation, err := f.store.Allocate(ctx, allocationRequest("drained-absent-authorized"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
			t.Fatal(err)
		}
		evidence := ReleaseEvidence{
			Kind: ReleaseEvidenceDrainedAbsentProxy, RunID: allocation.RunID, ProxyName: allocation.ProxyName,
			ProxyNotObserved: true, ConfirmedClientDisconnected: true,
		}
		if err := f.store.ConfirmReleased(ctx, evidence); err != nil {
			t.Fatalf("drained absent proxy release: %v", err)
		}
	})

	t.Run("never granted", func(t *testing.T) {
		f := newRedisFixture(t, nil)
		f.ready(t)
		ctx := context.Background()
		allocation, err := f.store.Allocate(ctx, allocationRequest("drained-absent-never-granted"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
			t.Fatal(err)
		}
		evidence := ReleaseEvidence{
			Kind: ReleaseEvidenceDrainedAbsentProxy, RunID: allocation.RunID, ProxyName: allocation.ProxyName,
			ProxyNotObserved: true, ConfirmedClientDisconnected: true,
		}
		if err := f.store.ConfirmReleased(ctx, evidence); !errors.Is(err, ErrReleaseUnconfirmed) {
			t.Fatalf("never-granted absent evidence accepted: %v", err)
		}
	})

	t.Run("invalid combinations", func(t *testing.T) {
		f := newRedisFixture(t, nil)
		f.ready(t)
		ctx := context.Background()
		allocation, err := f.store.Allocate(ctx, allocationRequest("drained-absent-invalid"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
			t.Fatal(err)
		}
		for name, mutate := range map[string]func(*ReleaseEvidence){
			"missing disconnected client": func(e *ReleaseEvidence) { e.ConfirmedClientDisconnected = false },
			"missing absent proxy":        func(e *ReleaseEvidence) { e.ProxyNotObserved = false },
			"mixed offline sample": func(e *ReleaseEvidence) {
				e.ObservedOffline, e.SampleAvailable = true, true
			},
			"active connection": func(e *ReleaseEvidence) { e.CurrentConnections = 1 },
		} {
			t.Run(name, func(t *testing.T) {
				evidence := ReleaseEvidence{
					Kind: ReleaseEvidenceDrainedAbsentProxy, RunID: allocation.RunID, ProxyName: allocation.ProxyName,
					ProxyNotObserved: true, ConfirmedClientDisconnected: true,
				}
				mutate(&evidence)
				if err := f.store.ConfirmReleased(ctx, evidence); !errors.Is(err, ErrReleaseUnconfirmed) {
					t.Fatalf("invalid evidence accepted: %+v err=%v", evidence, err)
				}
			})
		}
	})
}

func TestAnonymousPendingVerificationCarriesRegistrationEvidenceAndQuarantinesInvalidFlags(t *testing.T) {
	for _, test := range []struct {
		name        string
		grant       bool
		mutate      func(context.Context, *redisFixture, Allocation) error
		wantGranted bool
		wantCorrupt bool
	}{
		{name: "never granted"},
		{name: "granted", grant: true, wantGranted: true},
		{name: "missing", wantCorrupt: true, mutate: func(ctx context.Context, f *redisFixture, a Allocation) error {
			return f.client.HDel(ctx, f.store.runKey(a.RunID), "proxy_registration_granted").Err()
		}},
		{name: "malformed", wantCorrupt: true, mutate: func(ctx context.Context, f *redisFixture, a Allocation) error {
			return f.client.HSet(ctx, f.store.runKey(a.RunID), "proxy_registration_granted", "2").Err()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRedisFixture(t, nil)
			f.ready(t)
			ctx := context.Background()
			allocation, err := f.store.Allocate(ctx, allocationRequest("pending-"+test.name))
			if err != nil {
				t.Fatal(err)
			}
			if test.grant {
				if _, err := f.store.AuthorizeProxyRegistration(ctx, allocation.RunID, allocation.CredentialToken, allocation.ProxyName); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.RequestStop(ctx, allocation.RunID, allocation.CredentialToken); err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				if err := test.mutate(ctx, f, allocation); err != nil {
					t.Fatal(err)
				}
			}
			items, err := f.store.PendingVerification(ctx, 1)
			if test.wantCorrupt {
				if !errors.Is(err, ErrVerificationCorrupt) || len(items) != 0 {
					t.Fatalf("invalid flag pending=%+v err=%v", items, err)
				}
				return
			}
			if err != nil || len(items) != 1 {
				t.Fatalf("pending=%+v err=%v", items, err)
			}
			got := items[0].ProxyRegistrationGranted
			if got != test.wantGranted {
				t.Fatalf("pending registration evidence=%v want=%v", got, test.wantGranted)
			}
		})
	}
}
