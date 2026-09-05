package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type controlRunOperations interface {
	StartAccountRun(context.Context, domain.AccountStartCommand) (domain.StartResult, error)
	RedeemLaunchCode(context.Context, domain.LaunchRedeemCommand) (domain.StartResult, error)
	AuthorizeRun(context.Context, domain.RunProof) (domain.RunAuthorization, error)
	Heartbeat(context.Context, domain.RunProof) (domain.HeartbeatResult, error)
	RequestOwnedStop(context.Context, string, string) (domain.Run, error)
	RequestCredentialStop(context.Context, domain.RunProof) (domain.Run, error)
	ConfirmOnline(context.Context, domain.RunRegistrationEvidence) (domain.Run, error)
	ConfirmOffline(context.Context, domain.RunDisconnectEvidence) (domain.Run, error)
	Sweep(context.Context, int) (domain.SweepResult, error)
}

type controlRunHarness struct {
	store   *ControlPostgres
	api     controlRunOperations
	fixture controlTestFixture
	clock   *controlTestClock
	account domain.Account
	route   domain.Route
}

func newControlRunHarness(t *testing.T) *controlRunHarness {
	t.Helper()
	fixture := newControlTestFixture(t)
	clock := &controlTestClock{value: time.Date(2026, 9, 5, 4, 34, 56, 123456789, time.UTC)}
	fixture.Options.Now = clock.Now
	p, err := OpenControlPostgres(context.Background(), fixture.DSN, fixture.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	account, route := seedControlAccountRoute(t, fixture.DB)
	api, ok := any(p).(controlRunOperations)
	if !ok {
		t.Fatal("ControlPostgres does not implement the atomic run persistence contract")
	}
	return &controlRunHarness{store: p, api: api, fixture: fixture, clock: clock, account: account, route: route}
}

func (h *controlRunHarness) startCommand(key string) domain.AccountStartCommand {
	return domain.AccountStartCommand{AccountID: h.account.ID, RouteID: h.route.ID, IdempotencyKey: key, RequestIP: netip.MustParseAddr("192.0.2.15")}
}

func (h *controlRunHarness) start(t *testing.T, key string) domain.StartResult {
	t.Helper()
	result, err := h.api.StartAccountRun(context.Background(), h.startCommand(key))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (h *controlRunHarness) launch(t *testing.T) domain.LaunchRedeemCommand {
	t.Helper()
	const token = "nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err := h.fixture.DB.Exec(`INSERT INTO route_launch_codes (id,route_id,secret_hash,created_at,expires_at)
		VALUES ('nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa',$1,$2,$3,$4)`, h.route.ID,
		identity.HashToken(string(h.fixture.Options.LaunchPepper), token), h.route.CreatedAt, h.route.CreatedAt.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return domain.LaunchRedeemCommand{Token: token, Nonce: "launch-nonce", RequestIP: netip.MustParseAddr("192.0.2.15")}
}

func (h *controlRunHarness) online(t *testing.T, result domain.StartResult) domain.Run {
	t.Helper()
	run, err := h.api.ConfirmOnline(context.Background(), domain.RunRegistrationEvidence{
		RunID: result.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID,
		ConnectedIP: netip.MustParseAddr("198.51.100.8"), ObservedOnline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func controlProof(result domain.StartResult) domain.RunProof {
	return domain.RunProof{RunID: result.Run.ID, Token: result.CredentialToken}
}

func requireControlZeroStart(t *testing.T, result domain.StartResult, err error) {
	t.Helper()
	if err == nil || !reflect.DeepEqual(result, domain.StartResult{}) {
		t.Fatalf("failed start exposed a result: nonzero=%t err=%v", !reflect.DeepEqual(result, domain.StartResult{}), err)
	}
}

func requireControlCounts(t *testing.T, db *sql.DB, runs, credentials, replays int) {
	t.Helper()
	for table, want := range map[string]int{"tunnel_runs": runs, "run_credentials": credentials, "operation_replays": replays} {
		if got := controlTestTableCount(t, db, table); got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
}

func TestControlRunConcurrentStartsOccupyExactlyOneSlot(t *testing.T) {
	h := newControlRunHarness(t)
	const workers = 8
	gate := make(chan struct{})
	results := make(chan domain.StartResult, workers)
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for i := range workers {
		go func() {
			ready.Done()
			<-gate
			result, err := h.api.StartAccountRun(context.Background(), h.startCommand(fmt.Sprintf("start-%d", i)))
			results <- result
			errs <- err
		}()
	}
	ready.Wait()
	close(gate)
	var success, conflicts, usable int
	for range workers {
		if result := <-results; result.CredentialToken != "" {
			usable++
		}
		err := <-errs
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrRunAlreadyActive) {
			conflicts++
		} else {
			t.Errorf("concurrent start error=%v", err)
		}
	}
	if success != 1 || conflicts != 7 || usable != 1 {
		t.Fatalf("success=%d conflicts=%d usable=%d", success, conflicts, usable)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 1)
}

func TestControlRunConcurrentSameKeyReturnsOriginalCredential(t *testing.T) {
	h := newControlRunHarness(t)
	const workers = 6
	type outcome struct {
		result domain.StartResult
		err    error
	}
	gate := make(chan struct{})
	done := make(chan outcome, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-gate
			result, err := h.api.StartAccountRun(context.Background(), h.startCommand("same-key"))
			done <- outcome{result, err}
		}()
	}
	ready.Wait()
	close(gate)
	var first domain.StartResult
	var fresh, replayed int
	for range workers {
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first.Run.ID == "" {
			first = got.result
		}
		if got.result.Run.ID != first.Run.ID || got.result.CredentialToken != first.CredentialToken || got.result.CredentialToken == "" {
			t.Fatal("same key allocated a second run or lost its secret")
		}
		if got.result.Replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != 5 {
		t.Fatalf("fresh=%d replayed=%d", fresh, replayed)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 1)
}

func TestControlRunReplaySurvivesReopenWithoutRenewingDeadlines(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "lost-response")
	if first.Run.CreatedAt != h.route.CreatedAt || first.Run.ConnectDeadlineAt != h.route.CreatedAt.Add(2*time.Minute) || first.Run.LeaseExpiresAt != nil {
		t.Fatalf("initial absolute deadlines are wrong: %#v", first.Run)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := OpenControlPostgres(context.Background(), h.fixture.DSN, h.fixture.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	h.clock.Set(h.route.CreatedAt.Add(time.Minute))
	api := any(p).(controlRunOperations)
	got, err := api.StartAccountRun(context.Background(), h.startCommand("lost-response"))
	if err != nil || !got.Replayed || got.CredentialToken != first.CredentialToken || !reflect.DeepEqual(got.Run, first.Run) {
		t.Fatalf("durable replay failed: replayed=%t sameSecret=%t sameRun=%t err=%v", got.Replayed, got.CredentialToken == first.CredentialToken, reflect.DeepEqual(got.Run, first.Run), err)
	}
	if _, err := api.AuthorizeRun(context.Background(), controlProof(got)); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || bytes.Contains(encoded, []byte(got.CredentialToken)) {
		t.Fatal("public JSON leaked credential")
	}
	for _, table := range []string{"tunnel_accounts", "tunnel_routes", "tunnel_runs", "run_credentials", "operation_replays"} {
		rows, err := h.fixture.DB.Query("SELECT row_to_json(t)::text FROM " + table + " t")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, first.CredentialToken) || strings.Contains(raw, strings.Split(first.CredentialToken, ".")[1]) {
				t.Fatal("raw token persisted")
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var hash string
	var encrypted []byte
	if err := h.fixture.DB.QueryRow(`SELECT c.secret_hash, p.response_ciphertext FROM run_credentials c JOIN operation_replays p ON p.run_id=c.run_id`).Scan(&hash, &encrypted); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 || bytes.Contains(encrypted, []byte(first.CredentialToken)) {
		t.Fatal("credential was not hash/ciphertext only")
	}
}

func TestControlRunFailedReplaySaveRollsBackEveryStartWrite(t *testing.T) {
	h := newControlRunHarness(t)
	cmd := h.launch(t)
	if _, err := h.fixture.DB.Exec(`CREATE FUNCTION reject_run_replay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejects replay'; END $$;
		CREATE TRIGGER reject_run_replay BEFORE INSERT ON operation_replays FOR EACH ROW EXECUTE FUNCTION reject_run_replay()`); err != nil {
		t.Fatal(err)
	}
	result, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	requireControlZeroStart(t, result, err)
	requireControlCounts(t, h.fixture.DB, 0, 0, 0)
	var redeemed sql.NullTime
	if err := h.fixture.DB.QueryRow(`SELECT redeemed_at FROM route_launch_codes`).Scan(&redeemed); err != nil || redeemed.Valid {
		t.Fatalf("code consumed by rollback: %t %v", redeemed.Valid, err)
	}
	result, err = h.api.StartAccountRun(context.Background(), h.startCommand("failed-account"))
	requireControlZeroStart(t, result, err)
	requireControlCounts(t, h.fixture.DB, 0, 0, 0)
}

func TestControlRedeemActiveConflictDoesNotConsumeCode(t *testing.T) {
	h := newControlRunHarness(t)
	cmd := h.launch(t)
	h.start(t, "occupy")
	result, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	requireControlZeroStart(t, result, err)
	if !errors.Is(err, domain.ErrRunAlreadyActive) {
		t.Fatalf("active conflict=%v", err)
	}
	var redeemed sql.NullTime
	if err := h.fixture.DB.QueryRow(`SELECT redeemed_at FROM route_launch_codes`).Scan(&redeemed); err != nil || redeemed.Valid {
		t.Fatalf("code consumed: %t %v", redeemed.Valid, err)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 1)
}

func TestControlRedeemReplayRequiresFullSecretAndMatchingNonce(t *testing.T) {
	h := newControlRunHarness(t)
	cmd := h.launch(t)
	first, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*domain.LaunchRedeemCommand){
		"wrong secret": func(c *domain.LaunchRedeemCommand) { c.Token = c.Token[:len(c.Token)-1] + "E" },
		"hash only": func(c *domain.LaunchRedeemCommand) {
			c.Token = identity.HashToken(string(h.fixture.Options.LaunchPepper), c.Token)
		},
		"wrong namespace": func(c *domain.LaunchRedeemCommand) { c.Token = first.CredentialToken },
		"different nonce": func(c *domain.LaunchRedeemCommand) { c.Nonce = "other-nonce" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cmd
			mutate(&changed)
			got, err := h.api.RedeemLaunchCode(context.Background(), changed)
			requireControlZeroStart(t, got, err)
		})
	}
	got, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil || !got.Replayed || got.CredentialToken != first.CredentialToken {
		t.Fatalf("valid launch replay failed: %v", err)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 1)
}

func TestControlRedeemNearExpiryReplayOutlivesInitialCodeOnly(t *testing.T) {
	h := newControlRunHarness(t)
	cmd := h.launch(t)
	h.clock.Set(h.route.CreatedAt.Add(9*time.Minute + 59*time.Second))
	first, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Set(h.route.CreatedAt.Add(10*time.Minute + 30*time.Second))
	got, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil || !got.Replayed || got.CredentialToken != first.CredentialToken {
		t.Fatalf("committed replay could not outlive initial code: %v", err)
	}
	h.online(t, first)
	h.clock.Set(h.route.CreatedAt.Add(11*time.Minute + 59*time.Second))
	got, err = h.api.RedeemLaunchCode(context.Background(), cmd)
	requireControlZeroStart(t, got, err)
}

func TestControlRunReplayRequiresCurrentOwnerAndRunnableState(t *testing.T) {
	for _, state := range []string{"owner", "deleted", "stopped", "revoked", "expired"} {
		t.Run(state, func(t *testing.T) {
			h := newControlRunHarness(t)
			first := h.start(t, "protected-key")
			cmd := h.startCommand("protected-key")
			switch state {
			case "owner":
				if _, err := h.fixture.DB.Exec(`INSERT INTO tunnel_accounts(id,identity_issuer,identity_subject,created_at,last_seen_at)
					VALUES ('10000000-0000-4000-8000-000000000099','https://issuer.test','other-owner',$1,$1)`, h.route.CreatedAt); err != nil {
					t.Fatal(err)
				}
				cmd.AccountID = "10000000-0000-4000-8000-000000000099"
			case "deleted":
				_, err := h.fixture.DB.Exec(`UPDATE tunnel_routes SET status='deleted',deleted_at=$2,recoverable_until=$3 WHERE id=$1`, h.route.ID, h.route.CreatedAt, h.route.CreatedAt.Add(7*24*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
			case "stopped":
				if _, err := h.api.RequestOwnedStop(context.Background(), h.account.ID, h.route.ID); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if _, err := h.fixture.DB.Exec(`UPDATE run_credentials SET revoked_at=$1`, h.route.CreatedAt); err != nil {
					t.Fatal(err)
				}
			case "expired":
				h.online(t, first)
				h.clock.Set(h.route.CreatedAt.Add(90 * time.Second))
			}
			got, err := h.api.StartAccountRun(context.Background(), cmd)
			requireControlZeroStart(t, got, err)
		})
	}
}

func TestControlRunReplayIgnoresChangedTransportIPButBindsRoute(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "transport")
	cmd := h.startCommand("transport")
	cmd.RequestIP = netip.MustParseAddr("198.51.100.19")
	got, err := h.api.StartAccountRun(context.Background(), cmd)
	if err != nil || !got.Replayed || got.CredentialToken != first.CredentialToken || got.Run.RequestIP != first.Run.RequestIP {
		t.Fatalf("transport IP changed operation identity: %v", err)
	}
	const otherRoute = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := h.fixture.DB.Exec(`INSERT INTO tunnel_routes(id,account_id,protocol,subdomain,proxy_name,status,created_at,updated_at) VALUES($1,$2,'http','other-route',$1,'active',$3,$3)`, otherRoute, h.account.ID, h.route.CreatedAt); err != nil {
		t.Fatal(err)
	}
	cmd.RouteID = otherRoute
	got, err = h.api.StartAccountRun(context.Background(), cmd)
	requireControlZeroStart(t, got, err)
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("changed route was not fingerprint conflict: %v", err)
	}
}

func TestControlRedeemReplayIgnoresChangedTransportIP(t *testing.T) {
	h := newControlRunHarness(t)
	cmd := h.launch(t)
	first, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	cmd.RequestIP = netip.MustParseAddr("198.51.100.19")
	got, err := h.api.RedeemLaunchCode(context.Background(), cmd)
	if err != nil || !got.Replayed || got.CredentialToken != first.CredentialToken || got.Run.RequestIP != first.Run.RequestIP {
		t.Fatalf("launch transport IP changed identity: %v", err)
	}
}

func TestControlRunReplayRejectsCorruptAndSwappedMetadata(t *testing.T) {
	for _, mutation := range []string{"ciphertext", "request", "expiry", "route", "run"} {
		t.Run(mutation, func(t *testing.T) {
			h := newControlRunHarness(t)
			h.start(t, "metadata-key")
			var query string
			switch mutation {
			case "ciphertext":
				query = `UPDATE operation_replays SET response_ciphertext='broken'`
			case "request":
				query = `UPDATE operation_replays SET request_hash=repeat('a',64)`
			case "expiry":
				query = `UPDATE operation_replays SET expires_at=expires_at+interval '1 second'`
			case "route":
				query = `UPDATE operation_replays SET route_id=NULL`
			case "run":
				query = `UPDATE operation_replays SET run_id=NULL`
			}
			if _, err := h.fixture.DB.Exec(query); err != nil {
				t.Fatal(err)
			}
			got, err := h.api.StartAccountRun(context.Background(), h.startCommand("metadata-key"))
			requireControlZeroStart(t, got, err)
		})
	}
}

func TestControlRunAuthorizationVerifiesCompleteProofBeforeState(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "proof")
	proof := controlProof(first)
	if got, err := h.api.AuthorizeRun(context.Background(), proof); err != nil || got.CredentialID != first.CredentialID || got.Route.ID != h.route.ID {
		t.Fatalf("valid proof rejected: %v", err)
	}
	launch := h.launch(t)
	for name, bad := range map[string]domain.RunProof{
		"wrong secret":    {RunID: first.Run.ID, Token: first.CredentialToken[:len(first.CredentialToken)-1] + map[bool]string{true: "E", false: "A"}[first.CredentialToken[len(first.CredentialToken)-1] == 'A']},
		"hash only":       {RunID: first.Run.ID, Token: identity.HashToken(string(h.fixture.Options.RunPepper), first.CredentialToken)},
		"wrong run":       {RunID: "run_bbbbbbbbbbbbbbbbbbbbbbbbbb", Token: first.CredentialToken},
		"wrong namespace": {RunID: first.Run.ID, Token: launch.Token},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := h.api.AuthorizeRun(context.Background(), bad)
			if !errors.Is(err, domain.ErrInvalidRunProof) || !reflect.DeepEqual(got, domain.RunAuthorization{}) {
				t.Fatalf("invalid proof leaked state: %v", err)
			}
		})
	}
	h.clock.Set(first.Run.ConnectDeadlineAt)
	if _, err := h.api.AuthorizeRun(context.Background(), proof); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("unswept deadline allowed proof: %v", err)
	}
	bad := proof
	bad.Token = first.CredentialToken[:len(first.CredentialToken)-1] + map[bool]string{true: "E", false: "A"}[first.CredentialToken[len(first.CredentialToken)-1] == 'A']
	if _, err := h.api.AuthorizeRun(context.Background(), bad); !errors.Is(err, domain.ErrInvalidRunProof) {
		t.Fatalf("state tested before secret: %v", err)
	}
}

func TestControlRunHeartbeatAndRegistrationRespectExactBoundaries(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "heartbeat")
	proof := controlProof(first)
	h.clock.Set(h.route.CreatedAt.Add(time.Minute))
	hb, err := h.api.Heartbeat(context.Background(), proof)
	if err != nil || hb.Stopped || !hb.Run.ConnectDeadlineAt.Equal(first.Run.ConnectDeadlineAt) || hb.Run.LeaseExpiresAt != nil || hb.Run.LastHeartbeatAt == nil || !hb.Run.LastHeartbeatAt.Equal(h.route.CreatedAt.Add(time.Minute)) {
		t.Fatalf("starting heartbeat renewed deadline: %#v %v", hb, err)
	}
	online := h.online(t, first)
	if online.LeaseExpiresAt == nil || !online.LeaseExpiresAt.Equal(h.route.CreatedAt.Add(150*time.Second)) {
		t.Fatalf("initial lease=%v", online.LeaseExpiresAt)
	}
	h.clock.Set(h.route.CreatedAt.Add(100 * time.Second))
	reconnected := h.online(t, first)
	if !reconnected.LeaseExpiresAt.Equal(*online.LeaseExpiresAt) {
		t.Fatal("registration renewed established lease")
	}
	hb, err = h.api.Heartbeat(context.Background(), proof)
	if err != nil || hb.Run.LeaseExpiresAt == nil || !hb.Run.LeaseExpiresAt.Equal(h.route.CreatedAt.Add(190*time.Second)) {
		t.Fatalf("heartbeat did not extend valid lease: %#v %v", hb, err)
	}
	h.clock.Set(h.route.CreatedAt.Add(190 * time.Second))
	hb, err = h.api.Heartbeat(context.Background(), proof)
	if err != nil || !hb.Stopped {
		t.Fatalf("expired heartbeat=%#v err=%v", hb, err)
	}
	if _, err := h.api.AuthorizeRun(context.Background(), proof); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("expired lease authorized: %v", err)
	}
	if _, err := h.api.ConfirmOnline(context.Background(), domain.RunRegistrationEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ConnectedIP: netip.MustParseAddr("198.51.100.8"), ObservedOnline: true}); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("expired lease reconnected: %v", err)
	}
}

func TestControlRunStartingDeadlineCannotBeExtendedByHeartbeat(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "starting-boundary")
	h.clock.Set(first.Run.ConnectDeadlineAt.Add(-time.Microsecond))
	if _, err := h.api.AuthorizeRun(context.Background(), controlProof(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.Heartbeat(context.Background(), controlProof(first)); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(first.Run.ConnectDeadlineAt)
	if _, err := h.api.AuthorizeRun(context.Background(), controlProof(first)); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("exact deadline accepted: %v", err)
	}
	if _, err := h.api.ConfirmOnline(context.Background(), domain.RunRegistrationEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ConnectedIP: netip.MustParseAddr("198.51.100.8"), ObservedOnline: true}); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("late first registration accepted: %v", err)
	}
}

func TestControlRunStopRetainsSlotUntilExactOfflineEvidence(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "stop")
	h.online(t, first)
	proof := controlProof(first)
	evidence := domain.RunDisconnectEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOffline: true}
	if _, err := h.api.ConfirmOffline(context.Background(), evidence); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
		t.Fatalf("normal close finalized live run: %v", err)
	}
	stopped, err := h.api.RequestCredentialStop(context.Background(), proof)
	if err != nil || stopped.Status != domain.RunStopping || stopped.DesiredState != domain.DesiredStopped || stopped.StopRequestedAt == nil || stopped.StoppedAt != nil {
		t.Fatalf("stop=%#v err=%v", stopped, err)
	}
	for range 2 {
		again, err := h.api.RequestOwnedStop(context.Background(), h.account.ID, h.route.ID)
		if err != nil || !reflect.DeepEqual(again, stopped) {
			t.Fatalf("repeated owner stop changed result: %v", err)
		}
		again, err = h.api.RequestCredentialStop(context.Background(), proof)
		if err != nil || !reflect.DeepEqual(again, stopped) {
			t.Fatalf("repeated secret stop changed result: %v", err)
		}
	}
	got, err := h.api.StartAccountRun(context.Background(), h.startCommand("new-start"))
	requireControlZeroStart(t, got, err)
	if !errors.Is(err, domain.ErrRunAlreadyActive) {
		t.Fatalf("stopping slot released: %v", err)
	}
	if _, err := h.api.AuthorizeRun(context.Background(), proof); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("stopped proof authorized: %v", err)
	}
	for name, mutate := range map[string]func(*domain.RunDisconnectEvidence){
		"wrong route":          func(e *domain.RunDisconnectEvidence) { e.RouteID = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb" },
		"wrong proxy":          func(e *domain.RunDisconnectEvidence) { e.ProxyName = "wrong" },
		"not offline":          func(e *domain.RunDisconnectEvidence) { e.ObservedOffline = false },
		"connections":          func(e *domain.RunDisconnectEvidence) { e.CurrentConnections = 1 },
		"negative connections": func(e *domain.RunDisconnectEvidence) { e.CurrentConnections = -1 },
		"unknown run":          func(e *domain.RunDisconnectEvidence) { e.RunID = "run_bbbbbbbbbbbbbbbbbbbbbbbbbb" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := evidence
			mutate(&bad)
			if _, err := h.api.ConfirmOffline(context.Background(), bad); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
				t.Fatalf("bad evidence accepted: %v", err)
			}
		})
	}
	offline, err := h.api.ConfirmOffline(context.Background(), evidence)
	if err != nil || offline.Status != domain.RunOffline || offline.StoppedAt == nil {
		t.Fatalf("offline=%#v err=%v", offline, err)
	}
	if terminal, err := h.api.RequestCredentialStop(context.Background(), proof); err != nil || terminal.Status != domain.RunOffline {
		t.Fatalf("terminal secret stop=%#v %v", terminal, err)
	}
	h.start(t, "replacement")
	if terminal, err := h.api.ConfirmOffline(context.Background(), evidence); err != nil || terminal.ID != first.Run.ID || terminal.Status != domain.RunOffline {
		t.Fatalf("late evidence affected replacement: %#v %v", terminal, err)
	}
	requireControlCounts(t, h.fixture.DB, 2, 2, 2)
}

func TestControlRunOnlineRejectsUntrustedOrMismatchedEvidence(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "online-evidence")
	base := domain.RunRegistrationEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ConnectedIP: netip.MustParseAddr("198.51.100.8"), ObservedOnline: true}
	for name, mutate := range map[string]func(*domain.RunRegistrationEvidence){
		"wrong route": func(e *domain.RunRegistrationEvidence) { e.RouteID = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb" },
		"wrong proxy": func(e *domain.RunRegistrationEvidence) { e.ProxyName = "wrong" },
		"not online":  func(e *domain.RunRegistrationEvidence) { e.ObservedOnline = false },
		"invalid IP":  func(e *domain.RunRegistrationEvidence) { e.ConnectedIP = netip.Addr{} },
		"unknown run": func(e *domain.RunRegistrationEvidence) { e.RunID = "run_bbbbbbbbbbbbbbbbbbbbbbbbbb" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := base
			mutate(&bad)
			if _, err := h.api.ConfirmOnline(context.Background(), bad); !errors.Is(err, domain.ErrRunEvidenceInvalid) {
				t.Fatalf("bad registration accepted: %v", err)
			}
		})
	}
}

func TestControlRunStopAndReplaySerializeWithoutRevival(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "stop-race")
	gate := make(chan struct{})
	type replayOutcome struct {
		result domain.StartResult
		err    error
	}
	replayed := make(chan replayOutcome, 1)
	stopped := make(chan error, 1)
	go func() {
		<-gate
		got, err := h.api.StartAccountRun(context.Background(), h.startCommand("stop-race"))
		replayed <- replayOutcome{got, err}
	}()
	go func() {
		<-gate
		_, err := h.api.RequestCredentialStop(context.Background(), controlProof(first))
		stopped <- err
	}()
	close(gate)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	got := <-replayed
	if got.err == nil {
		if !got.result.Replayed || got.result.CredentialToken != first.CredentialToken {
			t.Fatal("race allocated second run")
		}
	} else {
		requireControlZeroStart(t, got.result, got.err)
	}
	if _, err := h.api.AuthorizeRun(context.Background(), controlProof(first)); !errors.Is(err, domain.ErrRunStopped) {
		t.Fatalf("stop race revived run: %v", err)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 1)
}

func TestControlRunFailedCommitReturnsNoCredentialAndDoesNotConsumeCode(t *testing.T) {
	for _, operation := range []string{"account", "launch"} {
		t.Run(operation, func(t *testing.T) {
			h := newControlRunHarness(t)
			cmd := h.launch(t)
			if _, err := h.fixture.DB.Exec(`CREATE FUNCTION reject_run_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejects commit'; END $$;
				CREATE CONSTRAINT TRIGGER reject_run_commit AFTER INSERT ON operation_replays DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_run_commit()`); err != nil {
				t.Fatal(err)
			}
			var result domain.StartResult
			var err error
			if operation == "account" {
				result, err = h.api.StartAccountRun(context.Background(), h.startCommand("commit-failure"))
			} else {
				result, err = h.api.RedeemLaunchCode(context.Background(), cmd)
			}
			requireControlZeroStart(t, result, err)
			if !strings.Contains(err.Error(), "fixture rejects commit") {
				t.Fatalf("failure was not deferred commit: %v", err)
			}
			requireControlCounts(t, h.fixture.DB, 0, 0, 0)
			var redeemed sql.NullTime
			if err := h.fixture.DB.QueryRow(`SELECT redeemed_at FROM route_launch_codes`).Scan(&redeemed); err != nil || redeemed.Valid {
				t.Fatalf("failed commit consumed code: %t %v", redeemed.Valid, err)
			}
		})
	}
}

func TestControlRedeemConcurrentSameNonceAcrossStores(t *testing.T) {
	h := newControlRunHarness(t)
	cmd := h.launch(t)
	other, err := OpenControlPostgres(context.Background(), h.fixture.DSN, h.fixture.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	const workers = 6
	type outcome struct {
		result domain.StartResult
		err    error
	}
	gate := make(chan struct{})
	done := make(chan outcome, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for i := range workers {
		api := h.api
		if i%2 == 1 {
			api = any(other).(controlRunOperations)
		}
		go func() {
			ready.Done()
			<-gate
			result, err := api.RedeemLaunchCode(context.Background(), cmd)
			done <- outcome{result, err}
		}()
	}
	ready.Wait()
	close(gate)
	var first domain.StartResult
	var fresh, replayed int
	for range workers {
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first.Run.ID == "" {
			first = got.result
		}
		if got.result.Run.ID != first.Run.ID || got.result.CredentialToken != first.CredentialToken || got.result.CredentialToken == "" {
			t.Fatal("concurrent launch allocated inconsistent credentials")
		}
		if got.result.Replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != 5 {
		t.Fatalf("fresh=%d replayed=%d", fresh, replayed)
	}
	requireControlCounts(t, h.fixture.DB, 1, 1, 1)
	var redeemed sql.NullTime
	if err := h.fixture.DB.QueryRow(`SELECT redeemed_at FROM route_launch_codes`).Scan(&redeemed); err != nil || !redeemed.Valid {
		t.Fatalf("successful launch was not consumed: %v", err)
	}
	var raw string
	if err := h.fixture.DB.QueryRow(`SELECT row_to_json(c)::text FROM route_launch_codes c`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, cmd.Token) || strings.Contains(raw, strings.Split(cmd.Token, ".")[1]) {
		t.Fatal("launch secret stored in plaintext")
	}
}

func TestControlRedeemReplaySurvivesReopenAndRejectsRevocationAndTampering(t *testing.T) {
	for _, mutation := range []string{"none", "revoked", "expired run", "ciphertext", "request", "association", "expiry"} {
		t.Run(mutation, func(t *testing.T) {
			h := newControlRunHarness(t)
			cmd := h.launch(t)
			first, err := h.api.RedeemLaunchCode(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.Close(); err != nil {
				t.Fatal(err)
			}
			p, err := OpenControlPostgres(context.Background(), h.fixture.DSN, h.fixture.Options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			api := any(p).(controlRunOperations)
			var query string
			switch mutation {
			case "revoked":
				query = `UPDATE route_launch_codes SET revoked_at=created_at`
			case "expired run":
				if _, err := api.ConfirmOnline(context.Background(), domain.RunRegistrationEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOnline: true, ConnectedIP: netip.MustParseAddr("198.51.100.8")}); err != nil {
					t.Fatal(err)
				}
				h.clock.Set(h.route.CreatedAt.Add(90 * time.Second))
			case "ciphertext":
				query = `UPDATE operation_replays SET response_ciphertext='corrupt'`
			case "request":
				query = `UPDATE operation_replays SET request_hash=repeat('b',64)`
			case "association":
				query = `UPDATE operation_replays SET run_id=NULL`
			case "expiry":
				query = `UPDATE operation_replays SET expires_at=expires_at+interval '1 second'`
			}
			if query != "" {
				if _, err := h.fixture.DB.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			got, err := api.RedeemLaunchCode(context.Background(), cmd)
			if mutation == "none" {
				if err != nil || !got.Replayed || got.CredentialToken != first.CredentialToken || !reflect.DeepEqual(got.Run, first.Run) {
					t.Fatalf("launch recovery failed: %v", err)
				}
			} else {
				requireControlZeroStart(t, got, err)
			}
		})
	}
}

func TestControlRedeemInitialExpiryAndRevocationAreEnforced(t *testing.T) {
	for _, state := range []string{"just valid", "exact expiry", "revoked"} {
		t.Run(state, func(t *testing.T) {
			h := newControlRunHarness(t)
			cmd := h.launch(t)
			var want error
			switch state {
			case "just valid":
				h.clock.Set(h.route.CreatedAt.Add(10*time.Minute - time.Microsecond))
			case "exact expiry":
				h.clock.Set(h.route.CreatedAt.Add(10 * time.Minute))
				want = domain.ErrLaunchCodeExpired
			case "revoked":
				if _, err := h.fixture.DB.Exec(`UPDATE route_launch_codes SET revoked_at=created_at`); err != nil {
					t.Fatal(err)
				}
				want = domain.ErrLaunchCodeRevoked
			}
			got, err := h.api.RedeemLaunchCode(context.Background(), cmd)
			if want == nil {
				if err != nil || got.CredentialToken == "" {
					t.Fatalf("code rejected before boundary: %v", err)
				}
			} else {
				requireControlZeroStart(t, got, err)
				if !errors.Is(err, want) {
					t.Fatalf("code state error=%v want=%v", err, want)
				}
			}
		})
	}
}

func TestControlRunInvalidInputsAndWrongPepperFailClosed(t *testing.T) {
	h := newControlRunHarness(t)
	for _, ip := range []netip.Addr{{}, netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("ff02::1"), netip.MustParseAddr("fe80::1%eth0")} {
		cmd := h.startCommand("invalid-IP")
		cmd.RequestIP = ip
		got, err := h.api.StartAccountRun(context.Background(), cmd)
		requireControlZeroStart(t, got, err)
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("invalid IP error=%v", err)
		}
	}
	cmd := h.startCommand("")
	got, err := h.api.StartAccountRun(context.Background(), cmd)
	requireControlZeroStart(t, got, err)
	first := h.start(t, "valid")
	wrongPepper := *h.store
	wrongPepper.runPepper = string(h.fixture.Options.LaunchPepper)
	api := any(&wrongPepper).(controlRunOperations)
	if _, err := api.AuthorizeRun(context.Background(), controlProof(first)); !errors.Is(err, domain.ErrInvalidRunProof) {
		t.Fatalf("wrong pepper authorized: %v", err)
	}
	if _, err := api.Heartbeat(context.Background(), controlProof(first)); !errors.Is(err, domain.ErrInvalidRunProof) {
		t.Fatalf("wrong pepper heartbeat: %v", err)
	}
	if _, err := api.RequestCredentialStop(context.Background(), controlProof(first)); !errors.Is(err, domain.ErrInvalidRunProof) {
		t.Fatalf("wrong pepper stopped run: %v", err)
	}
}

func TestControlRunReplayUsesVerifiedCanonicalAccountPrincipal(t *testing.T) {
	h := newControlRunHarness(t)
	const owner = "a0000000-0000-4000-8000-000000000001"
	if _, err := h.fixture.DB.Exec(`INSERT INTO tunnel_accounts(id,identity_issuer,identity_subject,created_at,last_seen_at)
		VALUES ($1,'https://issuer.test','canonical-owner',$2,$2)`, owner, h.route.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := h.fixture.DB.Exec(`UPDATE tunnel_routes SET account_id=$2 WHERE id=$1`, h.route.ID, owner); err != nil {
		t.Fatal(err)
	}
	cmd := h.startCommand("canonical-principal")
	cmd.AccountID = strings.ToUpper(owner)
	first, err := h.api.StartAccountRun(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	var principal string
	if err := h.fixture.DB.QueryRow(`SELECT principal_key FROM operation_replays`).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	if principal != owner {
		t.Fatalf("replay principal used unnormalized input: %q", principal)
	}
	cmd.AccountID = owner
	got, err := h.api.StartAccountRun(context.Background(), cmd)
	if err != nil || !got.Replayed || got.CredentialToken != first.CredentialToken {
		t.Fatalf("same verified account lost replay: %v", err)
	}
}

func TestControlRunReplayExpiresEvenWhenHeartbeatKeepsRunValid(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "replay-cutoff")
	h.online(t, first)
	h.clock.Set(h.route.CreatedAt.Add(time.Minute))
	if _, err := h.api.Heartbeat(context.Background(), controlProof(first)); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(h.route.CreatedAt.Add(2*time.Minute - time.Microsecond))
	got, err := h.api.StartAccountRun(context.Background(), h.startCommand("replay-cutoff"))
	if err != nil || !got.Replayed || !reflect.DeepEqual(got.Run, first.Run) {
		t.Fatalf("valid replay did not preserve original deadlines: %v", err)
	}
	h.clock.Set(h.route.CreatedAt.Add(2 * time.Minute))
	if _, err := h.api.AuthorizeRun(context.Background(), controlProof(first)); err != nil {
		t.Fatalf("heartbeat did not keep run valid at replay cutoff: %v", err)
	}
	got, err = h.api.StartAccountRun(context.Background(), h.startCommand("replay-cutoff"))
	requireControlZeroStart(t, got, err)
	if !errors.Is(err, identity.ErrReplayExpired) {
		t.Fatalf("valid run outlived replay expiry incorrectly: %v", err)
	}
}

func TestControlRunTimeIsResampledAfterAccountLockWait(t *testing.T) {
	for _, operation := range []string{"start", "authorize", "heartbeat", "online", "account replay", "launch redeem", "launch replay"} {
		t.Run(operation, func(t *testing.T) {
			h := newControlRunHarness(t)
			var first domain.StartResult
			var launch domain.LaunchRedeemCommand
			now := h.route.CreatedAt.Add(2 * time.Minute)
			switch operation {
			case "launch redeem":
				launch = h.launch(t)
				now = h.route.CreatedAt.Add(10 * time.Minute)
			case "launch replay":
				launch = h.launch(t)
				var err error
				first, err = h.api.RedeemLaunchCode(context.Background(), launch)
				if err != nil {
					t.Fatal(err)
				}
				h.online(t, first)
			case "start":
			default:
				first = h.start(t, "lock-wait")
			}
			var got domain.StartResult
			var hb domain.HeartbeatResult
			err := controlRunWithAccountLockWait(t, h, now, func(api controlRunOperations, ctx context.Context) error {
				var err error
				switch operation {
				case "start", "account replay":
					got, err = api.StartAccountRun(ctx, h.startCommand("lock-wait"))
				case "authorize":
					_, err = api.AuthorizeRun(ctx, controlProof(first))
				case "heartbeat":
					hb, err = api.Heartbeat(ctx, controlProof(first))
				case "online":
					_, err = api.ConfirmOnline(ctx, domain.RunRegistrationEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOnline: true, ConnectedIP: netip.MustParseAddr("198.51.100.8")})
				case "launch redeem", "launch replay":
					got, err = api.RedeemLaunchCode(ctx, launch)
				}
				return err
			})
			switch operation {
			case "start":
				if err != nil || !got.Run.CreatedAt.Equal(now) || !got.Run.ConnectDeadlineAt.Equal(now.Add(2*time.Minute)) {
					t.Fatalf("start used prelock clock: %#v %v", got.Run, err)
				}
			case "heartbeat":
				if err != nil || !hb.Stopped {
					t.Fatalf("heartbeat used prelock clock: %#v %v", hb, err)
				}
			case "launch redeem":
				requireControlZeroStart(t, got, err)
				if !errors.Is(err, domain.ErrLaunchCodeExpired) {
					t.Fatalf("redemption used prelock expiry: %v", err)
				}
			default:
				if err == nil {
					t.Fatal("operation authorized using prelock time")
				}
			}
		})
	}
}

func TestControlRunTimeIsResampledAfterCredentialLockWait(t *testing.T) {
	for _, operation := range []string{"authorize", "heartbeat", "online", "account replay", "launch replay"} {
		t.Run(operation, func(t *testing.T) {
			h := newControlRunHarness(t)
			var first domain.StartResult
			var launch domain.LaunchRedeemCommand
			if operation == "launch replay" {
				launch = h.launch(t)
				var err error
				first, err = h.api.RedeemLaunchCode(context.Background(), launch)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				first = h.start(t, "credential-lock")
			}
			h.online(t, first)
			h.clock.Set(h.route.CreatedAt.Add(89 * time.Second))
			var hb domain.HeartbeatResult
			err := controlRunWithLockWait(t, h, h.route.CreatedAt.Add(90*time.Second),
				`SELECT id FROM run_credentials WHERE run_id=$1 FOR UPDATE`, []any{first.Run.ID}, "FROM run_credentials",
				func(api controlRunOperations, ctx context.Context) error {
					var err error
					switch operation {
					case "authorize":
						_, err = api.AuthorizeRun(ctx, controlProof(first))
					case "heartbeat":
						hb, err = api.Heartbeat(ctx, controlProof(first))
					case "online":
						_, err = api.ConfirmOnline(ctx, domain.RunRegistrationEvidence{RunID: first.Run.ID, RouteID: h.route.ID, ProxyName: h.route.ID, ObservedOnline: true, ConnectedIP: netip.MustParseAddr("198.51.100.8")})
					case "account replay":
						_, err = api.StartAccountRun(ctx, h.startCommand("credential-lock"))
					case "launch replay":
						_, err = api.RedeemLaunchCode(ctx, launch)
					}
					return err
				})
			if operation == "heartbeat" {
				if err != nil || !hb.Stopped {
					t.Fatalf("heartbeat used precredential-lock clock: %#v %v", hb, err)
				}
			} else if !errors.Is(err, domain.ErrRunStopped) {
				t.Fatalf("operation used precredential-lock clock: %v", err)
			}
		})
	}
}

func TestControlRunTimeIsResampledAfterReplayLockWait(t *testing.T) {
	for _, operation := range []string{"account", "launch"} {
		t.Run(operation, func(t *testing.T) {
			h := newControlRunHarness(t)
			var first domain.StartResult
			var launch domain.LaunchRedeemCommand
			if operation == "launch" {
				launch = h.launch(t)
				var err error
				first, err = h.api.RedeemLaunchCode(context.Background(), launch)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				first = h.start(t, "replay-lock")
			}
			h.online(t, first)
			h.clock.Set(h.route.CreatedAt.Add(time.Minute))
			if _, err := h.api.Heartbeat(context.Background(), controlProof(first)); err != nil {
				t.Fatal(err)
			}
			h.clock.Set(h.route.CreatedAt.Add(119 * time.Second))
			var result domain.StartResult
			err := controlRunWithLockWait(t, h, h.route.CreatedAt.Add(2*time.Minute),
				`SELECT id FROM operation_replays WHERE run_id=$1 FOR UPDATE`, []any{first.Run.ID}, "FROM operation_replays",
				func(api controlRunOperations, ctx context.Context) error {
					var err error
					if operation == "launch" {
						result, err = api.RedeemLaunchCode(ctx, launch)
					} else {
						result, err = api.StartAccountRun(ctx, h.startCommand("replay-lock"))
					}
					return err
				})
			requireControlZeroStart(t, result, err)
			if !errors.Is(err, identity.ErrReplayExpired) {
				t.Fatalf("replay used prereplay-lock clock: %v", err)
			}
			if _, err := h.api.AuthorizeRun(context.Background(), controlProof(first)); err != nil {
				t.Fatalf("run not valid independently of replay: %v", err)
			}
		})
	}
}

func TestControlRunReplayRejectsSwappedCiphertext(t *testing.T) {
	h := newControlRunHarness(t)
	first := h.start(t, "first-response")
	const otherRoute = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := h.fixture.DB.Exec(`INSERT INTO tunnel_routes(id,account_id,protocol,subdomain,proxy_name,status,created_at,updated_at)
		VALUES($1,$2,'http','second-response',$1,'active',$3,$3)`, otherRoute, h.account.ID, h.route.CreatedAt); err != nil {
		t.Fatal(err)
	}
	cmd := h.startCommand("second-response")
	cmd.RouteID = otherRoute
	second, err := h.api.StartAccountRun(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.AuthorizeRun(context.Background(), domain.RunProof{RunID: second.Run.ID, Token: first.CredentialToken}); !errors.Is(err, domain.ErrInvalidRunProof) {
		t.Fatalf("credential authorized a different existing run: %v", err)
	}
	var ciphertext []byte
	if err := h.fixture.DB.QueryRow(`SELECT response_ciphertext FROM operation_replays WHERE run_id=$1`, second.Run.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if _, err := h.fixture.DB.Exec(`UPDATE operation_replays SET response_ciphertext=$2 WHERE run_id=$1`, first.Run.ID, ciphertext); err != nil {
		t.Fatal(err)
	}
	got, err := h.api.StartAccountRun(context.Background(), h.startCommand("first-response"))
	requireControlZeroStart(t, got, err)
	if !errors.Is(err, identity.ErrInvalidReplayCiphertext) {
		t.Fatalf("swapped ciphertext error=%v", err)
	}
}

type controlRunLockTracer struct {
	once    sync.Once
	started chan struct{}
	match   string
}

func (tracer *controlRunLockTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, tracer.match) && strings.Contains(data.SQL, "FOR UPDATE") {
		tracer.once.Do(func() { close(tracer.started) })
	}
	return ctx
}

func (*controlRunLockTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func controlRunWithAccountLockWait(t *testing.T, h *controlRunHarness, now time.Time, operation func(controlRunOperations, context.Context) error) error {
	t.Helper()
	return controlRunWithLockWait(t, h, now, `SELECT id FROM tunnel_accounts WHERE id=$1 FOR UPDATE`, []any{h.account.ID}, "FROM tunnel_accounts", operation)
}

func controlRunWithLockWait(t *testing.T, h *controlRunHarness, now time.Time, query string, args []any, match string, operation func(controlRunOperations, context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocker, err := h.fixture.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var locked string
	if err := blocker.QueryRowContext(ctx, query, args...).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	tracer := &controlRunLockTracer{started: make(chan struct{}), match: match}
	config, err := pgx.ParseConfig(h.fixture.DSN)
	if err != nil {
		t.Fatal(err)
	}
	config.Tracer = tracer
	db := stdlib.OpenDB(*config)
	defer db.Close()
	p := *h.store
	p.db = db
	done := make(chan error, 1)
	go func() { done <- operation(any(&p).(controlRunOperations), ctx) }()
	select {
	case <-tracer.started:
	case <-ctx.Done():
		t.Fatal("operation never attempted target lock")
	}
	h.clock.Set(now)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("operation did not finish after releasing target lock")
		return ctx.Err()
	}
}
