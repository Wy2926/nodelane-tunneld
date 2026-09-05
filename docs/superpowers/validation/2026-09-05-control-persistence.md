# Control Persistence Local Validation

## Status

- Implementation, joined automated checks, whole-branch review, and final fix re-review passed locally on 2026-09-05.
- Reviewed task commits: infrastructure `a94adad`, account/routes `49a1db7`, runs/replay `3554cb2`, and panic cleanup `8009b8d`.
- Joined cross-operation tests: `eb86b71`, on local `codex/auth-console`.
- Final reviewed code: `7598f5d`, including atomic confirmed-offline credential revocation. No open data-layer review findings remain.
- This is the registered persistent data layer only. The legacy store and server wiring have not been switched.

## Delivered Boundary

`store.ControlPostgres` adds a separate fresh-only Goose schema and transactional account, route, launch, run, replay, and cleanup operations. The seven tables are `tunnel_accounts`, `tunnel_routes`, `route_launch_codes`, `tunnel_runs`, `run_credentials`, `operation_replays`, and `network_bans`.

PostgreSQL constraints and account/route-first locks enforce the five-active-route default, unreleased-name uniqueness, exact seven-day recovery cutoff, and one active run per route. Run creation, hash-only credentials, optional launch consumption, and encrypted startup response commit together. All errors return no partial start result; uncertain commit outcomes are not retried internally.

Authorization checks full bearer secrets and current route/run/credential state. Time is sampled after relevant locks; starting deadlines, established leases, replay expiry, and terminal retention are checked at exact cutoffs. Expiry requests stopping without inventing data-plane disconnection. Trusted matching offline evidence atomically records credential revocation, finalizes the run, and releases the active slot. Failed revocation rolls back the whole transition; repeated terminal calls preserve the original timestamps. Cleanup preserves required replay/hash state and never removes permanent routes.

No traffic-history, request-log, billing, HTTP visitor-limit, or subscription table was added. Registered state is not duplicated into Redis. Anonymous Redis state and runtime integration remain later phases.

## Verification

All database commands used only `NODELANE_TEST_DATABASE_URL` pointing to a disposable owned fixture. The tests never fall back to `DATABASE_URL`. The helper checks effective loopback connection settings, database `nodelane_control_test`, the server marker `nodelane.test_marker=control_fixture_v1`, and server address before creating a uniquely quoted test schema.

Fixture image: `postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73`; observed server version `17.11`, UTC, loopback-only host port, tmpfs data. No production database was touched.

| Check | Environment | Result |
| --- | --- | --- |
| `go test ./... -count=1` | Go 1.27.0, Windows amd64, real PostgreSQL | Passed on final reviewed code; store suite 43.655s |
| `go test ./internal/store -run 'TestControlIntegration\|TestControlRunConfirmOffline' -count=10` | Windows, real PostgreSQL | Passed on final reviewed code; 24.348s |
| `go vet ./...` | Windows | Passed |
| `gofmt` and `git diff --check` | Changed Go files and joined diff | Passed; Git's existing CRLF checkout advisories are not whitespace defects |
| `go test -mod=readonly -race ./... -count=1` | Go 1.27.1, Linux amd64, CGO enabled, `TERM=dumb`, real PostgreSQL | Passed on final reviewed code; store suite 49.518s |

Windows has `CGO_ENABLED=0`; no Windows race pass is claimed. Linux uses a separate disposable compiler container with the source mounted read-only. Its supported headless terminal configuration is explicit below.

After the final test, the owned fixture contained zero `ctl_test_` schemas. Both disposable test containers were stopped and removed after their exact IDs and ownership labels were rechecked. Temporary task worktrees were removed only after clean-tree and merged-ancestor checks; their commits remain in the integration branch.

The joined tests cover delete versus account start/redemption, restoration versus start, stopped-state preservation through restoration, stop versus both replay mechanisms, and name transfer versus stale ownership/proof/evidence. Per-task tests additionally cover real migration interleavings, precise constraint failures, quota/name races, response-loss recovery after reopening the store, failed commits, final-lock expiry, ciphertext/metadata tampering, and concurrent cleanup. Expectations use literal states/counts and controlled barriers rather than sleeps.

## Headless CLI Finding

An initial Linux full-race command with no `TERM` failed in the existing `TestRunTargetFormReturnsQuietCancellation`. A zero-width buffer-backed terminal message causes a negative input width in the unchanged `huh`/`bubbles` stack. The secondary nil-model panic is fallout, not a data race.

The same focused failure was reproduced from the pre-persistence `6ce61cf` snapshot, both current and baseline pass with `TERM=dumb`, and current also fails without `-race`. CLI source and rendering dependency versions/checksums are unchanged. CLI code and rendering dependencies were not modified during diagnosis. Default unset-TERM headless CLI robustness remains work for the CLI phase; the configured Linux result above is not unconditional terminal or installer acceptance.

## Clarified Semantics

Request IP is validated transport metadata, not an idempotency fingerprint or identity. A changed valid source IP can recover the same authenticated operation; the original response, IP, and deadlines remain unchanged. Fingerprints include the resolved route ID for account starts and resolved code/route IDs for launch redemption. Future operation-affecting parameters must join that contract. Authentication and current ownership are rechecked on every request.

## Not Yet Accepted

- OIDC/BFF/session middleware, public request/response DTOs, and API cutover.
- Redis-only anonymous allocation, frps plugin integration, and verified native observations/statistics.
- Real Logto/Resend/Google/SSO/Device Flow and source-ban adapter integration.
- CLI/UI changes, 12-locale/RTL UI acceptance, and exact public Linux/PowerShell/CMD installer commands.
- Production migration, backup/restore rehearsal, provider configuration, secret rotation, publication, or deployment.

The next implementation phase consumes the reviewed repository methods through narrow use-case interfaces. It must not treat internal account IDs, trusted evidence structs, or replay keys as public authentication.
