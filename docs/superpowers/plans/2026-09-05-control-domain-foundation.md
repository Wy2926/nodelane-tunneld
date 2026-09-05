# Control Domain Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish the persistence-neutral route/run state predicates, route policy, new opaque credential formats, and authenticated replay encryption required by the new control-plane schema.

**Architecture:** Extend the existing domain and identity packages with focused new files, and introduce a small route-policy consumer package. Keep the legacy application wiring unchanged until the new transactional repositories and account API are ready. This foundation is the first part of spec phase 2; it does not claim that PostgreSQL transactions, APIs, or rollout are implemented.

**Tech Stack:** Go `1.27.0`, the existing identity primitives, and the Go standard library (`crypto/rand`, `crypto/aes`, `crypto/cipher`, encoding packages, `testing`). No new runtime or dependency is needed for these two tasks.

**Spec:** `../specs/2026-09-05-nodelane-auth-tunnel-console-design.md`, approved after commit `b1f68aa`; sections 2, 6, 7, 11.2, 13, and 20 apply.

## Global Constraints

- Work in `D:/Project/nodelane/nodelane-tunneld/.worktrees/auth-console`, branch `codex/auth-console`, not the main checkout.
- Permanent routes support `http` only, with `max_routes=5` by default. The route-count safety cap is not an HTTP visitor-concurrency limit.
- Public labels are 3-32 lowercase ASCII letters, digits, or hyphens, with alphanumeric ends. Reject Unicode, dots, full domains, Punycode, reserved labels, and the entire `anon-` prefix.
- Required reserved labels: `www`, `auth`, `api`, `admin`, `console`, `status`, `support`, `mail`, `smtp`, `frp`, `tunnel`.
- Permanent proxy names equal their `rte_` route ID. Anonymous proxy names use `anon_`.
- Deletion releases the active-route count immediately; names remain reserved for 7 days. An expired or released name cannot be restored.
- A route has at most one run in `starting`, `online`, or `stopping`. First connection deadline is 2 minutes; an established run's lease is 90 seconds. These are state deadlines, not client-cooperative forced-disconnection promises.
- Launch secrets and run secrets contain at least 256 random bits. Use separate peppers in production. A launch secret is not an account credential and a run credential controls only one run.
- Replay results use AEAD with authenticated operation, principal, key hash, request hash, route/run IDs, and absolute expiry. The business replay window is 2 minutes and is enforced by the later transaction layer; this cipher never extends expiry.
- Replay expiry uses UTC and exact microsecond precision, matching PostgreSQL `TIMESTAMPTZ`. The transaction layer samples/truncates its timestamp before sealing and persists that exact value; the cipher rejects sub-microsecond expiry instead of silently changing it.
- No frps patches, HTTP visitor limits, Prometheus, history, billing, new OIDC implementation, or production database operation.
- Do not modify `internal/store/postgres.go`, `internal/store/schema.sql`, existing API handlers, or `cmd/tunneld/main.go` in this foundation. Their coordinated replacement belongs to later phases.
- Preserve current tests and legacy behavior until the planned API cutover; do not make new parsers accept legacy `ftc` credentials.

## Boundary Decision

Use exclusive expiry for restoration, matching ROUTE-09's requirement that the original route is no longer recoverable when the 7-day interval ends: `now < recoverable_until`. The earlier inclusive phrase in the state description must be aligned with that rule. Exact-boundary tests are required; there is no additional grace interval.

## Task 1: Domain State and Route Policy

**Files:**
- Create `internal/domain/control_models.go`, `internal/domain/control_state.go`, `internal/domain/control_errors.go`, `internal/domain/control_state_test.go`.
- Create `internal/routes/policy.go`, `internal/routes/policy_test.go`.
- Modify only the restoration-boundary wording in the spec to remove its inclusive/exclusive contradiction.

**Interfaces:**
- Existing `domain.Client`, `domain.Tunnel`, `domain.Repository`, and `domain.LeaseManager` remain unchanged.
- New domain model structs mirror the approved schema. Use strings for opaque IDs and the database UUID representation; use `time.Time` for required timestamps and `*time.Time` for nullable timestamps; use `netip.Addr` for request/connection IPs, where an invalid connected address means absent.
- Models: `Account`, `Route`, `LaunchCode`, `Run`, `RunCredential`, `OperationReplay`. Account fields are `ID`, `IdentityIssuer`, `IdentitySubject`, `CreatedAt`, `LastSeenAt`. All other exported field names are PascalCase versions of the spec columns, including `ConnectDeadlineAt`, `LeaseExpiresAt`, `NameReleasedAt`, `ResponseCiphertext`, and `PrincipalKey`.
- Secret-bearing fields such as `SecretHash` and `ResponseCiphertext` carry `json:"-"` as defense in depth. They are not public DTOs.
- Enum types/constants: `RouteStatus` with `RouteActive`/`RouteDeleted`; `RunStatus` with `RunStarting`/`RunOnline`/`RunStopping`/`RunOffline`; `DesiredState` with `DesiredRunning`/`DesiredStopped`; `StartedVia` with `StartedViaDeviceLogin`/`StartedViaLaunchCode`.
- Define operation constants `OperationCreateRoute`, `OperationStartRun`, `OperationRedeemLaunch` with values `create_route`, `start_run`, `redeem_launch` for `OperationReplay.Operation` (string).
- Define domain errors used here: `ErrInvalidRequest`, `ErrSubdomainInvalid`, `ErrSubdomainReserved`, `ErrRouteLimitReached`, `ErrProtocolNotAllowed`. Do not overload the legacy generic conflict/limit error with these meanings.

Required state/policy signatures:

```go
func (r Route) RecoverableAt(now time.Time) bool
func (r Run) OccupiesActiveSlot() bool
func (r Run) AllowsConnectionAt(route Route, credential RunCredential, now time.Time) bool

type RoutePolicy struct {
    MaxRoutes int
    AllowedProtocols []string
}
func (p RoutePolicy) CheckCreate(protocol string, activeCount int) error
type RoutePolicyProvider interface {
    Policy(context.Context, string) (RoutePolicy, error)
}
type StaticPolicyProvider struct { maxRoutes int }
func NewStaticPolicyProvider(maxRoutes int) (*StaticPolicyProvider, error)
func (p *StaticPolicyProvider) Policy(ctx context.Context, accountID string) (RoutePolicy, error)
func ValidateSubdomain(label string) error
```

The policy types/functions live in `internal/routes`; state methods live in `internal/domain`. `AllowsConnectionAt` is explicitly a state predicate, not secret authentication: its comment and later consumers must require the bearer hash to have been verified separately in the same authorization operation.

- [ ] **Step 1: Write failing state and policy tests.**

Use fixed UTC time and literal expected outcomes, no sleeping. The minimum behavior matrix is:

| Case | Expected |
| --- | --- |
| Valid HTTP label `demo`, three-character `a-1`, 32-character lowercase label | Allowed |
| Uppercase, leading/trailing hyphen, too short/long, Unicode, dot/full host, `xn--` prefix | `ErrSubdomainInvalid` |
| Every required reserved label and valid `anon-demo` | `ErrSubdomainReserved` |
| HTTP creation with four active routes under policy five | Allowed |
| HTTP creation with five or six active routes | `ErrRouteLimitReached` |
| TCP or UDP permanent route even with spare capacity | `ErrProtocolNotAllowed` |
| Non-positive policy maximum or negative active count | `ErrInvalidRequest` |
| A caller mutates a returned protocol slice then asks the provider again | Provider still returns HTTP-only policy |
| Deleted unreleased route immediately before recovery cutoff | Recoverable |
| At/after cutoff, missing cutoff, active route, or released name | Not recoverable |
| Run starting/online/stopping | Occupies slot |
| Run offline or unknown status | Does not occupy slot |

For `AllowsConnectionAt`, build a valid route/run/credential fixture, then independently mutate these conditions and expect false: route mismatch, credential run mismatch, empty identities, deleted route, desired stopped, stopping/offline/unknown run status, revoked credential, expired or missing applicable deadline, online run with no successful first connection. Before first connection use only `ConnectDeadlineAt`; after a connection use only `LeaseExpiresAt`, including reconnection. A future lease must not rescue an expired initial connection deadline. Exactly at either deadline is expired.

Example using the new state interfaces:

```go
func TestRecoveryEndsAtDeadline(t *testing.T) {
    deadline := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
    route := Route{Status: RouteDeleted, RecoverableUntil: &deadline}
    if !route.RecoverableAt(deadline.Add(-time.Nanosecond)) {
        t.Fatal("route should be recoverable before cutoff")
    }
    if route.RecoverableAt(deadline) {
        t.Fatal("route must not be recoverable at cutoff")
    }
}
```

- [ ] **Step 2: Observe RED.**

Run `go test ./internal/domain ./internal/routes`. Resolve test syntax/import errors; the remaining failures must identify the missing state/policy behavior. Record the commands and failure evidence.

- [ ] **Step 3: Implement the small predicates and policy.**

`RecoverableAt` requires deleted status, no release timestamp, a cutoff, and strict `Before`. `AllowsConnectionAt` requires non-empty matching route/run/credential identities, active route, desired running, starting or online state, and an unrevoked credential. Distinguish a never-connected run from an established/reconnecting run with `ConnectedAt`; use its applicable strict deadline without mutating any timestamps. Online with no `ConnectedAt` is invalid. Do not verify a secret by comparing ordinary strings in this method.

Implement `ValidateSubdomain` using explicit ASCII syntax plus the reserved label/prefix sets. Do not lowercase or trim invalid input into a different resource. The static provider keeps its maximum private and returns an independent HTTP-only slice. It does not store account entitlements or query a database.

In the spec's derived route states, change only the phrase saying the current time is not later than the recovery deadline to the strict-before rule required by ROUTE-09. Record this boundary clarification in the progress ledger.

- [ ] **Step 4: Verify.**

Run `go test ./internal/domain ./internal/routes`, then `go test ./...`, `go vet ./...`, and `git diff --check`. The legacy application must still compile and its existing tests must pass.

- [ ] **Step 5: Commit and task-review.**

Commit with `feat: add control domain state and route policy`. The reviewer checks only this task plus the intentional one-line spec clarification. No database or API cutover is included.

## Task 2: Opaque Credentials and Replay AEAD

**Files:** Create `internal/identity/opaque.go`, `internal/identity/opaque_test.go`, `internal/identity/replay.go`, `internal/identity/replay_test.go`.

**Interfaces:**

Consumes `domain.OperationCreateRoute = "create_route"`, `domain.OperationStartRun = "start_run"`, and `domain.OperationRedeemLaunch = "redeem_launch"`, published by Task 1. These constants remain defined only in the domain package.

```go
type OpaqueCredential struct {
    ID string
    Token string `json:"-"`
}
func NewRouteID() (string, error)
func NewRunID() (string, error)
func NewLaunchCredential() (OpaqueCredential, error)
func NewRunCredential() (OpaqueCredential, error)
func ParseLaunchCredential(token string) (string, error)
func ParseRunCredential(token string) (string, error)

type ReplayContext struct {
    Operation string `json:"operation"`
    PrincipalKey string `json:"principal_key"`
    KeyHash string `json:"key_hash"`
    RequestHash string `json:"request_hash"`
    RouteID string `json:"route_id"`
    RunID string `json:"run_id"`
    ExpiresAt time.Time `json:"expires_at"`
}
type ReplayCipher struct { aead cipher.AEAD }
func NewReplayCipher(key []byte) (*ReplayCipher, error)
func (c *ReplayCipher) Seal(ctx ReplayContext, plaintext []byte) ([]byte, error)
func (c *ReplayCipher) Open(ctx ReplayContext, ciphertext []byte, now time.Time) ([]byte, error)
```

- Route/run IDs reuse `NewID` with prefixes `rte_`/`run_` and 16 random bytes.
- Launch wire format: `nlc_<26 lowercase base32 characters>.<43 canonical base64url characters encoding 32 random bytes>`.
- Run wire format: `nrc_<26 lowercase base32 characters>.<43 canonical base64url characters encoding 32 random bytes>`.
- Parsers return only the public credential ID and error. Namespaces are disjoint, and legacy `ftc`/`ctk_` values are rejected.
- Reuse existing `HashToken` and `TokenHashEqual` for full-token HMAC comparison. Separate production peppers are injected by the later configuration/service layer; do not introduce a fallback pepper.
- Errors: `ErrInvalidCredential`, `ErrInvalidReplayKey`, `ErrInvalidReplayContext`, `ErrInvalidReplayCiphertext`, `ErrReplayExpired` in `internal/identity`.

- [ ] **Step 1: Write failing token and replay tests.**

Test fresh credentials for proper prefix, decoded ID/secret lengths, namespace-specific parse roundtrip, and distinct generated values. Reject wrong prefixes, legacy tokens, whitespace, separators beyond the single dot, malformed ID characters/length, malformed or non-canonical base64url, and decoded secret lengths other than 32 bytes. Use literal synthetic credentials as parser fixtures so generation and parsing cannot mask each other's format errors.

For replay, use independent fixed 32-byte test keys, a fixed UTC cutoff, and a plaintext JSON response containing a synthetic run token. Required tests:

1. Constructor rejects empty and non-32-byte keys.
2. Seal/Open roundtrip returns the exact original bytes before expiry.
3. Repeated sealing produces different envelopes; plaintext is not directly present in ciphertext.
4. Mutating each authenticated field independently causes Open to fail with no plaintext.
5. Wrong key, truncated/malformed envelope, and modified ciphertext/tag all fail without panics or partial plaintext.
6. Open exactly at and after expiry returns `ErrReplayExpired`; a retry never changes the original cutoff.
7. Equivalent timestamps expressed in different time zones authenticate identically after UTC normalization.
8. Context with empty operation/principal/hash/route, unsupported operation, zero or sub-microsecond expiry, or a missing run ID for a run-producing operation is rejected. `create_route` permits an empty run ID; `start_run` and `redeem_launch` require one.

Example for independent field binding:

```go
changed := ctx
changed.PrincipalKey = "another-account"
plain, err := cipher.Open(changed, sealed, now)
if err == nil || len(plain) != 0 {
    t.Fatal("replay must remain bound to its authenticated principal")
}
```

- [ ] **Step 2: Observe RED.**

Run `go test ./internal/identity`. Record meaningful failures before implementing acceptance logic; keep the existing identity tests intact.

- [ ] **Step 3: Implement with existing primitives and standard AEAD.**

Use cryptographic randomness and raw URL-safe base64 for the new secrets. Validate public ID characters and exact length; validate the decoded secret length and canonical encoding. Do not accept another token namespace as a compatibility fallback. Generation failures return no usable token and do not log secret material.

Use AES-256-GCM from the standard library. Require `ExpiresAt.Nanosecond()%1000 == 0`; callers must compute the persisted deadline at microsecond precision before sealing. Serialize the `ReplayContext` struct through `encoding/json` after copying its expiry to UTC; do not concatenate ad hoc AAD strings. The envelope is the fresh GCM nonce followed by ciphertext and authentication tag. Reject envelopes shorter than nonce plus tag before slicing. Open checks the original context expiry and authenticates all fields before returning bytes. It does not change context, renew leases, create a new response, or perform database/network access.

Context validation uses the operation values defined by this plan. Hash fields are 64-character lowercase hexadecimal digests. Required route/run IDs must match their respective opaque-ID namespaces. Bound context field sizes before marshaling; use 256 bytes for `PrincipalKey` and the fixed sizes of the remaining identifier/digest fields. This layer is not an OIDC/JWT implementation.

Use the operation constants from `internal/domain` as the single definition of their string values. The domain state package must not import identity, so this does not introduce a cycle. Optional association IDs are empty strings in these Go models; database null conversion belongs to the subsequent SQL adapter.

- [ ] **Step 4: Verify.**

Run `go test ./internal/identity`, `go test ./...`, `go vet ./...`, and `git diff --check`. In a supported CI environment run the targeted identity/domain tests with the race detector. Record an unavailable race environment as not run, not passing.

- [ ] **Step 5: Commit and independently review.**

Commit with `feat: add scoped run credentials and replay encryption`. Review token parsing, secret exclusion, AAD binding, failure results, and expiry independently before moving to the SQL repository plan.

## Completion and Next Contract

This foundation is complete only after both tasks pass their focused/full checks and independent review. It produces no public endpoint and changes no running database. The next persistence plan consumes these exact models, policy interfaces, token formats, and `ReplayCipher`; it must supply the Goose fresh-schema guard, real PostgreSQL integration harness, account/route transactions, atomic run/code/replay operations, authorization, and cleanup before spec phase 2 is considered complete.

The later constructor must be separate from legacy `store.OpenPostgres`; do not silently point that function at the new schema while `cmd/tunneld` still expects the old repository. The final API cutover removes the legacy model and wiring together; no old compatibility path is promised for release.
