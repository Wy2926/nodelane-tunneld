# Control Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the new PostgreSQL schema and transactional account, route, launch, run, replay, and cleanup operations with real database concurrency tests.

**Architecture:** Add a separate `store.ControlPostgres` library over the existing pgx/database-sql driver. It consumes the reviewed domain/policy/identity foundation, uses Goose for fresh-schema initialization, and keeps all multi-row invariants inside transactions. Keep the legacy constructor and server wiring unchanged until the subsequent API cutover.

**Tech Stack:** Go `1.27.0`, existing `pgx/v5`, Goose `v3.28.0`, standard-library cryptography through the new identity package, and an isolated PostgreSQL 17 test container.

**Spec:** `../specs/2026-09-05-nodelane-auth-tunnel-console-design.md`; sections 2, 6, 7, 9, 13, 17, and 20. Foundation code is at `e225eed` on `codex/auth-console`.

## Global Constraints

- Task 1 runs in `D:/Project/nodelane/nodelane-tunneld/.worktrees/auth-console`. Tasks 2/3 run in separate task worktrees created from its reviewed infrastructure commit, then integrate back into `codex/auth-console`. User authorized parallel implementation on fixed contracts; owner files remain exclusive.
- No production DSN, existing database deletion, legacy data import, automatic downgrade, API cutover, frps patch, HTTP visitor cap, traffic history, or new identity provider.
- HTTP routes only; default five active routes per account. Lowercase labels 3-32 characters; reserved labels and `anon-` remain rejected by the existing policy.
- Route names remain reserved until seven days after deletion, with exact-cutoff expiry. Permanent `proxy_name` equals route ID.
- Initial connection deadline: 2 minutes. Established lease: 90 seconds. Launch code initial redemption: 10 minutes. Replay and terminal retention: 2 minutes.
- Sample operation time after acquiring relevant locks and normalize to UTC microseconds before persistence and replay sealing.
- Lock order: involved accounts sorted by ID; involved routes sorted by ID; launch codes sorted by ID; run; credential; replay. Candidate discovery is nonlocking and is revalidated after locks.
- No hash-only or expired-but-unswept record can authorize a run. Call the reviewed state predicate only after full bearer hash verification.
- Replays require the current operation's authentication/ownership context; the repository receives server-derived account IDs, not public request bodies. Launch redemption verifies the complete launch token itself.
- Unknown commit outcome is not permission to allocate twice. The caller retries the same operation/key; the transaction stores its result before returning.
- Test database only: `nodelane_control_test`, loopback access, and server setting `nodelane.test_marker=control_fixture_v1`. The test helper checks all three before creating or deleting any test schema.
- Pinned test image: `postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73`.

## Shared Contracts

Task 1 publishes these contracts before Tasks 2 and 3 compile. Put command/result structs in `internal/domain/control_commands.go` to avoid a store/routes import cycle. Strings used for optional associations mean absent when empty. All token/key material fields are excluded from JSON.

```go
type RouteQuery struct { Deleted bool }
type CreateRouteCommand struct {
    AccountID, Protocol, Subdomain string
    IdempotencyKey string `json:"-"`
}
type CreateRouteResult struct { Route Route; Replayed bool }
type IssueLaunchCodeCommand struct { AccountID, RouteID string }
type IssuedLaunchCode struct {
    Code LaunchCode
    Token string `json:"-"`
}
type AccountStartCommand struct {
    AccountID, RouteID string
    IdempotencyKey string `json:"-"`
    RequestIP netip.Addr
}
type LaunchRedeemCommand struct {
    Token string `json:"-"`
    Nonce string `json:"-"`
    RequestIP netip.Addr
}
type StartResult struct {
    Run Run
    CredentialID string
    CredentialToken string `json:"-"`
    Replayed bool
}
type RunProof struct { RunID string; Token string `json:"-"` }
type RunAuthorization struct { Route Route; Run Run; CredentialID string }
type HeartbeatResult struct { Run Run; Stopped bool }
type RunRegistrationEvidence struct {
    RunID, RouteID, ProxyName string
    ConnectedIP netip.Addr
    ObservedOnline bool
}
type RunDisconnectEvidence struct {
    RunID, RouteID, ProxyName string
    ObservedOffline bool
    CurrentConnections int64
}
type SweepResult struct {
    ExpiredRuns, DeletedRuns, DeletedCodes, DeletedReplays int
}
```

`RunRegistrationEvidence` and `RunDisconnectEvidence` are trusted internal coordinator inputs, not public DTOs or proof supplied by a client. The later frps adapter is responsible for obtaining verified native observations. A normal close notification alone must not finalize a still-running, unexpired reconnectable run.

`RequestIP` is validated transport metadata, not part of an idempotency request fingerprint. Initial commit records it; retries from a different valid source IP recover the original response, including its original IP and deadlines. The current account-start fingerprint contains the resolved route ID; launch redemption contains the resolved code ID and route ID. Keys/nonces are hashed separately, and bearer secrets never enter the request payload. Future operation-affecting fields must join the typed fingerprint. Each request still requires current authentication/ownership checks.

Additional domain sentinels in `control_storage_errors.go`: `ErrRouteNotFound`, `ErrRouteDeleted`, `ErrSubdomainConflict`, `ErrRunAlreadyActive`, `ErrRunStopped`, `ErrLaunchCodeExpired`, `ErrLaunchCodeUsed`, `ErrLaunchCodeRevoked`, `ErrIdempotencyConflict`, `ErrInvalidRunProof`, `ErrRunEvidenceInvalid`. Reuse existing foundation errors where appropriate.

## Task 1: Fresh Migrations, Constructor, and Isolated DB Tests

**Exclusive Files:** `go.mod`, `go.sum`; `internal/domain/control_commands.go`, `control_storage_errors.go`; `internal/store/control_postgres.go`, `control_migrations.go`, `control_tx.go`, `control_rows.go`, `control_replays.go`, `control_postgres_test.go`, `control_test_helpers_test.go`; `internal/store/migrations/00001_control.sql`.

**Produced Store Contract:**

```go
type ControlOptions struct {
    Now func() time.Time
    Policy routes.RoutePolicyProvider
    LaunchPepper, RunPepper, ReplayKey []byte
}
type ControlPostgres struct {
    db *sql.DB
    clock func() time.Time
    policy routes.RoutePolicyProvider
    replayCipher *identity.ReplayCipher
    launchPepper, runPepper string
}
func OpenControlPostgres(ctx context.Context, dsn string, opts ControlOptions) (*ControlPostgres, error)
func MigrateControlDatabase(ctx context.Context, db *sql.DB) error
func (p *ControlPostgres) Close() error
func (p *ControlPostgres) nowUTC() time.Time
func (p *ControlPostgres) withControlTx(ctx context.Context, fn func(*sql.Tx) error) error
func lockControlAccounts(ctx context.Context, tx *sql.Tx, ids ...string) error
type controlScanner interface { Scan(...any) error }
func scanControlRoute(row controlScanner) (domain.Route, error)
func scanControlRun(row controlScanner) (domain.Run, error)
func controlDigest(value string) string
func controlRequestDigest(value any) (string, error)
func (p *ControlPostgres) readControlReplay(ctx context.Context, tx *sql.Tx, operation, principal, keyHash string, forUpdate bool) (domain.OperationReplay, error)
func (p *ControlPostgres) saveControlReplay(ctx context.Context, tx *sql.Tx, replay domain.OperationReplay, plaintext []byte) error
func (p *ControlPostgres) openControlReplay(replay domain.OperationReplay, now time.Time) ([]byte, error)
```

Freeze column-list constants `controlRouteColumns` and `controlRunColumns` alongside scanners. `controlRunColumns` converts inet columns with `host(...)` so invalid nullable connected IP can map to the zero `netip.Addr`.

Replay helpers remain unexported. They do not authorize callers or renew deadlines: their owning use-case transaction must perform current authorization and lock-order checks first. `readControlReplay` returns `sql.ErrNoRows` for absence. Digests use SHA-256 plus lowercase hex, with typed JSON for request data. `saveControlReplay` seals the supplied metadata/payload and inserts the row; `openControlReplay` reconstructs exactly the authenticated metadata and decrypts. Callers generate `rpl_` IDs with the existing random-ID helper and supply the original microsecond timestamps. This shared code prevents route and run workers from implementing incompatible replay storage.

- [ ] **Step 1: Write failing migration/configuration tests and the guarded test helper.**

The helper reads only `NODELANE_TEST_DATABASE_URL`; unset means an explicitly reported integration skip. Parse its effective connection settings with `pgx.ParseConfig`, not only the URI. Reject non-loopback effective hosts/fallback hosts, another database, query overrides for host/hostaddr/port/dbname/database/user/password, `options`, or any startup override of `nodelane.test_marker`. Before DDL, verify live `current_database()`, the server marker, and `inet_server_addr()` against the private/loopback fixture boundary (Docker's server-side address may be private rather than loopback). Never allow a DSN to set the marker itself. Create a unique `ctl_test_` schema per test, quoted with `pgx.Identifier.Sanitize()`, and set `search_path` through a parsed DSN query parameter only after the guard. Register cleanup for only that generated schema and close pools before schema removal. Never read `DATABASE_URL` as a fallback.

Publish these shared test-only signatures for both subsequent worktrees:

```go
type controlTestFixture struct { DB *sql.DB; DSN string; Options ControlOptions }
func newControlTestFixture(t *testing.T) controlTestFixture
func newControlTestStore(t *testing.T) (*ControlPostgres, *sql.DB)
func seedControlAccountRoute(t *testing.T, db *sql.DB) (domain.Account, domain.Route)
type controlTestClock struct { mu sync.Mutex; value time.Time }
func (c *controlTestClock) Now() time.Time
func (c *controlTestClock) Set(value time.Time)
```

`seedControlAccountRoute` uses literal valid SQL fixture data and a fresh route ID, not Task 2's operations, so Task 3 is independently testable. Every caller closes its store before the fixture cleanup. Focused `go test -run` still compiles an entire package; separate worktrees prevent incomplete peer tests from invalidating RED/GREEN evidence.

Required tests: fresh initialization; repeat migration with no pending version; concurrent initializers; legacy table/row remains intact when initialization is refused; unrelated pre-existing tables refused; partially initialized schema refused; database version newer than embedded migration refused; invalid/reused/short secret keys rejected before opening/migrating; route uniqueness, proxy-name equality, protocol/status constraints, active-run partial uniqueness, and replay unique key enforced by PostgreSQL.

Representative assertion:

```go
if err := MigrateControlDatabase(ctx, db); err != nil { t.Fatal(err) }
if err := MigrateControlDatabase(ctx, db); err != nil { t.Fatal(err) }
var version int64
if err := db.QueryRowContext(ctx,
    "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil {
    t.Fatal(err)
}
if version != 1 { t.Fatalf("migration version = %d", version) }
```

- [ ] **Step 2: Run RED against the isolated fixture.**

The controller may start the pinned test image with a generated name, a task-owner label, tmpfs PostgreSQL data, UTC, the exact database/marker above, a synthetic password, and a loopback-only ephemeral host port. This is the only permitted database target. Capture its ID and port through structured Docker inspect data. Test code must not start or delete arbitrary containers. No production database is initialized.

Run `go test ./internal/store -run TestControl -count=1` with the explicit fixture DSN. Missing production APIs/schema must be the failure; do not accept a skipped suite as RED or GREEN.

- [ ] **Step 3: Implement migration and shared infrastructure.**

Add Goose `github.com/pressly/goose/v3 v3.28.0`. Use an embedded migration FS and the verified provider API:

```go
locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 30))
if err != nil { return err }
provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
    goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
if err != nil { return err }
_, err = provider.Up(ctx)
return err
```

`WithLockTimeout` arguments are seconds and attempt count. Do not call global `goose.SetDialect` or rely on `HasPending` as a synchronization primitive.

Reject legacy `clients`, `client_tokens`, or `tunnels`, unrelated schema objects, unsupported partial/newer schema state before applying new business DDL. Repeat the legacy/unrelated-object guard in migration 1 so a check/create gap cannot turn into compatibility migration. Existing Goose version metadata alone at version zero is a permissible fresh-initialization state. Use ordinary `CREATE TABLE`, not `IF NOT EXISTS` hiding schema drift. The Down migration must fail explicitly with a manual-restore message, not drop data or silently alter migration metadata.

Create all seven approved tables from spec section 6: `tunnel_accounts`, `tunnel_routes`, `route_launch_codes`, `tunnel_runs`, `run_credentials`, `operation_replays`, `network_bans`. Account UUIDs may use PostgreSQL's built-in `gen_random_uuid()`. Required text identifiers/hashes, foreign keys, enum checks, timestamp relationships, and nullable fields match the reviewed domain models. Constrain permanent proxy names to equal their route ID, HTTP-only labels to the policy syntax/reserved namespace, and lowercase 64-character hashes.

Critical indexes:

```sql
CREATE UNIQUE INDEX control_routes_unreleased_name_uq
    ON tunnel_routes (lower(subdomain)) WHERE name_released_at IS NULL;
CREATE UNIQUE INDEX control_runs_active_route_uq
    ON tunnel_runs (route_id) WHERE status IN ('starting', 'online', 'stopping');
CREATE UNIQUE INDEX control_replay_key_uq
    ON operation_replays (operation, principal_key, key_hash);
```

Add indexes for account route listing, expiry scans, and run/replay associations. No audit, request, traffic-history, subscription, or HTTP visitor-limit table.

Validate options before connection: peppers at least 32 bytes, replay key exactly 32 bytes, all purposes distinct. Copy retained pepper data; never log it or a DSN. A nil clock defaults to `time.Now`; nil policy defaults to `NewStaticPolicyProvider(5)`. Constructor uses a bounded existing-style database/sql pool, initializes the new schema, and closes the pool on failure. It does not replace legacy `OpenPostgres` or application wiring.

`withControlTx` begins/rolls back/commits and retries the complete callback at most three times only for SQLSTATE `40001` or `40P01`, respecting context. It never retries an uncertain connection/commit outcome. `lockControlAccounts` sorts/deduplicates IDs, locks existing rows in that order, and rejects missing owners. Scanners handle nullable values without hiding scan errors. Sample `nowUTC()` after locks, using UTC microsecond precision.

- [ ] **Step 4: Verify and publish the shared contract.**

Run all non-skipped `TestControl` integration tests plus existing `go test ./...` and `go vet ./...`. Confirm the fixture remains the only database touched. Publish constructor/command/transaction/scanner contracts for Tasks 2/3; do not alter them independently after those workers start.

- [ ] **Step 5: Commit and independently review.**

The controller stages only Task 1 paths and commits `feat: add fresh control schema and migration guard`. Reports remain ignored, not force-added. The user-authorized parallel batch for Tasks 2/3 begins after this common infrastructure passes its task gate.

## Task 2: Account and Route Transactions

**Exclusive Files:** `internal/store/control_accounts.go`, `control_routes.go`, `control_launch_codes.go`, `control_routes_test.go`.

**Produced Methods:**

```go
func (p *ControlPostgres) ResolveAccount(ctx context.Context, issuer, subject string) (domain.Account, error)
func (p *ControlPostgres) ListRoutes(ctx context.Context, accountID string, query domain.RouteQuery) ([]domain.Route, error)
func (p *ControlPostgres) GetRoute(ctx context.Context, accountID, routeID string) (domain.Route, error)
func (p *ControlPostgres) CreateRoute(ctx context.Context, cmd domain.CreateRouteCommand) (domain.CreateRouteResult, error)
func (p *ControlPostgres) DeleteRoute(ctx context.Context, accountID, routeID string) (domain.Route, error)
func (p *ControlPostgres) RestoreRoute(ctx context.Context, accountID, routeID string) (domain.Route, error)
func (p *ControlPostgres) IssueLaunchCode(ctx context.Context, cmd domain.IssueLaunchCodeCommand) (domain.IssuedLaunchCode, error)
func (p *ControlPostgres) ReleaseExpiredNames(ctx context.Context, limit int) (int, error)
```

- [ ] **Step 1: Write failing real-DB tests.**

Verify issuer+subject projection idempotence/concurrency and no email identity key; owned/foreign route access; five-route limit even under concurrent creation; same-label races across accounts; deleted entries excluded from quota; stop/delete distinction; restoration before/at cutoff and at capacity; expired-label transfer to a different account with a different route/proxy ID; old owner cannot restore after transfer; launch code hash-only storage, ten-minute expiry, active-run rejection and delete revocation.

Creation replay tests use the same account/key/body twice and require the same route ID with `Replayed=true`, one row, and no second quota charge. Same key with different body must return `ErrIdempotencyConflict`; a deleted resource is not recreated through replay. Use literal expected row counts, not the implementation's own helpers as expected results.

- [ ] **Step 2: Observe RED with the explicit test DSN.**

Run `go test ./internal/store -run 'TestControlAccount|TestControlRoute|TestControlLaunchIssue' -count=1`. Do not run the full suite while the run worker is editing; the controller performs the join gate.

- [ ] **Step 3: Implement atomic account/route operations.**

Projection upserts only `(identity_issuer, identity_subject)` and updates last-seen without changing identity. Every account-facing route query scopes SQL by `account_id`; missing and foreign rows both return `ErrRouteNotFound`.

Creation/restoration lock the account before counting active rows, then apply the existing policy and validate the label. For lazy cross-account expired-name release, discover the previous holder without locking, lock all involved accounts sorted, then routes sorted, and revalidate the holder before updating. Never take another account lock after holding a route lock. Unique constraints remain the final arbiter; map named constraint failures to precise domain errors.

Creation uses `identity.NewRouteID()` and `proxy_name=id`. Deletion marks the route deleted, sets the exact seven-day recovery interval, requests stopping for its active run, and revokes unused launch codes in one transaction; it does not claim the data plane disconnected. Restoration never clears a run's stopped desired state or revives revoked codes. Background name release uses the same ordering and predicates as lazy release.

Hash the idempotency key and a typed JSON request containing protocol/label. The server-derived account ID is the replay principal. Store the encrypted created-route response in the same transaction, with microsecond expiry and all AAD associations; lock/recheck current ownership before replay. Return no partial success result on any transaction/commit error.

Issue new launch codes only for an owned active route with no active run, using the reviewed generator and the launch pepper. Return plaintext only in the typed result, never store or log it.

- [ ] **Step 4: Verify focused tests and publish the cleanup method.**

Run the focused real-DB suite and `gofmt` on owned files, then the full suite in this task's own worktree. `ReleaseExpiredNames` is a route-lifecycle operation for the later coordinator; the run worker does not depend on it. Commit only owner paths on the task branch; the controller reviews and integrates task commits in sequence.

- [ ] **Step 5: Controller commit and independent review.**

Commit message: `feat: persist account and permanent route lifecycle`. Review quota/name/replay/delete interactions, not just CRUD success.

## Task 3: Run Authorization, Atomic Replay, and Cleanup

**Exclusive Files:** `internal/store/control_run_start.go`, `control_run_auth.go`, `control_run_lifecycle.go`, `control_cleanup.go`, `control_runs_test.go`, `control_cleanup_test.go`.

**Produced Methods:**

```go
func (p *ControlPostgres) StartAccountRun(ctx context.Context, cmd domain.AccountStartCommand) (domain.StartResult, error)
func (p *ControlPostgres) RedeemLaunchCode(ctx context.Context, cmd domain.LaunchRedeemCommand) (domain.StartResult, error)
func (p *ControlPostgres) AuthorizeRun(ctx context.Context, proof domain.RunProof) (domain.RunAuthorization, error)
func (p *ControlPostgres) Heartbeat(ctx context.Context, proof domain.RunProof) (domain.HeartbeatResult, error)
func (p *ControlPostgres) RequestOwnedStop(ctx context.Context, accountID, routeID string) (domain.Run, error)
func (p *ControlPostgres) RequestCredentialStop(ctx context.Context, proof domain.RunProof) (domain.Run, error)
func (p *ControlPostgres) ConfirmOnline(ctx context.Context, evidence domain.RunRegistrationEvidence) (domain.Run, error)
func (p *ControlPostgres) ConfirmOffline(ctx context.Context, evidence domain.RunDisconnectEvidence) (domain.Run, error)
func (p *ControlPostgres) Sweep(ctx context.Context, limit int) (domain.SweepResult, error)
```

- [ ] **Step 1: Write failing real-DB concurrency and expiry tests.**

Require one successful concurrent start per route, code not consumed on active-slot conflict, and no usable result on a failed transaction. For launch replay, verify the complete secret again, distinguish the same nonce from a different nonce, recover the same credential after discarding the original response and reopening the store, and reject expired/corrupt/swapped replay metadata. Changed valid transport IP must preserve the original replay. The current launch body has no additional operation-affecting field; verify changed request hashes/associations through tamper rejection, without inventing a body parameter. Account-start same-key/different-route requests must conflict. Include redemption near code expiry: its valid committed two-minute replay may outlive initial code expiry but cannot outlive its own expiry or current run validity.

Test account-start replay with current ownership, exact connection/lease boundaries, expired-but-unswept credentials, wrong run/token namespace, starting heartbeats not extending first-connect deadline, established heartbeats extending only valid leases, stop/delete versus replay races, repeated stops, late evidence, and cleanup retention. Raw SQL confirms only hashes and ciphertext are stored, with no raw token in text/bytea fields.

- [ ] **Step 2: Observe RED.**

Run `go test ./internal/store -run 'TestControlRun|TestControlRedeem|TestControlCleanup' -count=1` with the explicit guarded test DSN. Required external operations are against the isolated fixture only.

- [ ] **Step 3: Implement transactionally.**

Parse and hash full run/launch tokens using the correct namespace and pepper. Account IDs are trusted internal inputs from the future authentication layer; never accept a replay key as authentication. Nonlocking lookup may discover parent IDs, but locks and all current-state checks must be reacquired in canonical order before mutation or decryption.

Start transactions occupy the unique slot, create the run and hash-only credential, consume a launch code when applicable, and save the AEAD response before commit. Use the code ID as launch replay principal and the verified account ID as account-start principal. Compute request hashes internally from resolved route/code identifiers and operation-affecting request parameters, not caller-supplied hash values. Transport source IP is validated and stored but is not a new identity.

Use a private encryption-only replay payload with an explicit token field: directly marshaling `domain.StartResult` would omit its `json:"-"` secret and break credential recovery. Decode that private payload only after current route/run/credential/replay validity has been checked under locks. Return the same stored token and original absolute deadlines; replay never renews a run. Any error returns the zero result, not previously generated partial credentials.

`AuthorizeRun` verifies the full hash and matching IDs before the reviewed state predicate, using a consistent joined snapshot or locked transaction. Heartbeat resamples time after locks and uses the same predicate. Starting heartbeats update last-seen only; first confirmed online sets the initial lease; later confirmed reconnects cannot renew a lease without an API heartbeat. Normal expired/stopped runs cannot reconnect, even before cleanup.

Stop operations change desired state to stopped and status to stopping while retaining the slot. Matching-secret repeated stop/terminal responses remain narrow and cannot reconnect/decrypt replay. `ConfirmOnline` requires exact run/route/proxy association, positive trusted evidence, and an unexpired runnable state. `ConfirmOffline` requires exact evidence, offline observation, zero observed active connections, and a stop/expiry decision; an ordinary close while desired-running and within lease does not finalize a reconnectable run. Unknown or mismatched evidence fails closed.

Sweep first discovers candidates without row locks, then processes bounded items in canonical lock order. Expiry moves runs to stopping and denies further authorization, never fabricates disconnection. Delete only offline runs after stopped-at plus two minutes and all associated replay windows have elapsed; delete credentials/replays in foreign-key-safe order. Expired launch codes remain while a valid launch replay still needs their hash. Name release is the separate `ReleaseExpiredNames` operation owned by Task 2; the later coordinator invokes both, so this task does not require peer code to compile. Never delete an active route, an active slot, or a credential needed by an unexpired replay.

- [ ] **Step 4: Join and verify all persistence behavior.**

Run focused and full tests in this task's separate worktree. After both reviewed task branches integrate, the controller runs all `TestControl` real-DB tests, `go test ./... -count=1`, `go vet ./...`, and whitespace checks on the joined branch. Add a controller-owned integration test file for create/delete/restore versus start/stop/replay races that require both task APIs together. Concurrency tests use barriers and exact success/row-count assertions, not sleeps. Where Windows CGO is unavailable, run race checks in an isolated supported Linux environment or report the gap explicitly.

- [ ] **Step 5: Controller commit and broad data-layer review.**

Commit message: `feat: persist atomic run authorization and replay`. Review the combined persistence range for lock order and cross-operation races after per-task reviews. Fix any load-bearing findings before API integration. Keep test evidence and the isolated fixture identity in the ledger; stop/remove only the test container owned by this execution.

## Exit Gate

This plan completes the registered persistent data-layer portion only. Redis anonymous state, OIDC/BFF middleware, public API DTOs, frps native probes, CLI changes, UI, and real deployment acceptance remain separate phases. Do not activate the new store in the existing legacy server as a shortcut or claim the overall product is complete.
