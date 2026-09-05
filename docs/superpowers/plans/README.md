# Auth and Tunnel Console Delivery Index

**Authority:** `../specs/2026-09-05-nodelane-auth-tunnel-console-design.md`, approved by the user after commit `b1f68aa`.

This index coordinates the independently executable phase plans required by section 18 of the approved spec. It is not a replacement for those plans. A phase gets its own file, task interfaces, failing tests, implementation steps, and review gate before its code is changed. Completed local checks never substitute for staging or production evidence.

## Binding Scope

- Use stock `frp v0.70.0` at `7b6e01f04f286632f0d23715aa17a3bc41234b5c`. No frps patches, custom collectors, or replacement data plane.
- Use Logto for email verification codes, Google, OIDC, Device Flow, and explicit identity binding. Resend is the SMTP provider.
- No HTTP visitor-concurrency limit, Prometheus deployment, traffic history, graphs, request-log product, account billing, or GB quota.
- Preserve the five-active-route safety cap, one active run per route, seven-day name isolation, and anonymous allocation limits.
- Anonymous HTTP public labels start with `anon-`; permanent proxy names equal their `rte_` route IDs; anonymous proxy names start with `anon_`.
- Registered launch/run/credential creation and encrypted two-minute replay results commit in one PostgreSQL transaction. Anonymous allocation and encrypted replay results are atomic in Redis.
- Logout revokes the refresh token and clears local credentials. Previously issued access tokens may expire naturally.
- Retain 12 locales, Arabic RTL, keyboard support, reduced motion, and independent Linux/PowerShell/CMD acceptance.
- Follow section 20 of the spec for module boundaries, tests, generated assets, and review.
- No production database deletion, external publication, secret rotation, or live identity-provider mutations without the separate approval required by the spec.

## Phase Map

| Phase | Owner | Independently Reviewed Deliverable | Acceptance Ownership |
| --- | --- | --- | --- |
| 1. Identity deployment | `D:/Project/nodelane/auth` | Pinned Logto deployment, separate initialization, offline preflight, identity contract, operator setup and staging checklist | AUTH-01 through AUTH-06; AC-AUTH-01 through AC-AUTH-04; identity portions of AC-SEC-03 and AC-OPS-03 |
| 2. Persistent domain | `nodelane-tunneld` | Goose initial schema, focused repositories, route policy, immutable identities, state predicates, atomic replay, cleanup | ROUTE-03 through ROUTE-09; LAUNCH-01 through LAUNCH-03; MIG-03/MIG-04; AC-ROUTE-02 through AC-ROUTE-06; AC-LAUNCH-01 through AC-LAUNCH-05; AC-OPS-01 |
| 3. BFF and account API | `nodelane-tunneld` | OIDC/session integration, CSRF, ownership, route/run APIs, replay authorization, stable DTOs/errors | AUTH-02/AUTH-04/AUTH-05; CLI-02; AC-AUTH-04/AC-AUTH-05; AC-CLI-02; AC-SEC-01/AC-SEC-02; AC-ARCH-01/AC-ARCH-02 |
| 4. Anonymous and frps | `nodelane-tunneld` | Redis allocation, stock HTTP-plugin authorization, finite leases, cooperative stopping, fail-closed resource reconciliation | ANON-01 through ANON-06; ROUTE-05; CLI-03; AC-ANON-01 through AC-ANON-05; AC-RUN-01 through AC-RUN-04; AC-ARCH-03/AC-ARCH-04 |
| 5. CLI and installers | `nodelane-tunneld` | Explicit command dispatch, Device Flow, isolated credential stores, one-shot launch, preflight, three-shell parameter transport | LAUNCH-04/LAUNCH-05; CLI-01 through CLI-04; AC-CLI-01 through AC-CLI-04; AC-ANON-02; AC-RUN-02/AC-RUN-03 |
| 6. Current native statistics | `nodelane-tunneld` | Fixed-endpoint frps API adapter, account whitelist, nullable current snapshots, UTC day semantics, no history | STAT-01 through STAT-06; AC-STAT-01 through AC-STAT-06; AC-ARCH-04 |
| 7. Product UI | `nodelane-www` and `nodelane-tunneld/web` | Static main-site entry, protected console shell, lifecycle UI, typed translations, rebuilt embedded assets | AUTH-07; UI-01 through UI-03; ROUTE-01/ROUTE-02/ROUTE-10; AC-UI-01 through AC-UI-03; AC-ARCH-01/AC-ARCH-02 |
| 8. Integration and release | All three owners | End-to-end staging evidence, backup/restore rehearsal, manual production maintenance runbook, exact public commands | MIG-01 through MIG-04; AC-SEC-01 through AC-SEC-04; AC-OPS-01 through AC-OPS-04; all cross-phase staging/production acceptance |

`SCOPE-01` and `SCOPE-02` bind every phase. The acceptance mapping assigns ownership; an overlapping ID still needs one complete end-to-end evidence record.

## Local Delivery Status

| Phase | Local Evidence | Remaining Acceptance |
| --- | --- | --- |
| Identity deployment | Local auth main at `a22e375`; offline configuration/contract checks passed, 80 tests | Real Resend, Google, SSO, Device Flow, and deployment remain unverified |
| Persistent domain | [Foundation](2026-09-05-control-domain-foundation.md) and [persistence](2026-09-05-control-persistence.md) implemented; reviewed data layer fast-forwarded into local main at `fe46d4d`, with real-PostgreSQL regressions repeated before and after merge | Library is not wired into legacy server/API; no production database initialized |
| BFF/API and later phases | Separate phase plans remain the next work | Runtime integration, CLI/UI, native statistics, staging and production |

The [local persistence validation record](../validation/2026-09-05-control-persistence.md) includes exact commands, the Linux `TERM=dumb` prerequisite, the inherited headless CLI limitation, and the boundary between local evidence and live acceptance.

## First Executable Plan

The first phase plan is `D:/Project/nodelane/auth/docs/superpowers/plans/2026-09-05-logto-bootstrap.md`. All paths inside it are relative to the auth project root unless explicitly stated otherwise.

The user approved the product spec, isolated worktrees, and the local auth and Tunnel data-layer main merges. Further implementation continues in an isolated worktree. Remote publication and deployment have not been authorized.

## Execution and Evidence

- Use task-sized implementation and independent spec/quality review; keep a per-plan progress ledger.
- Prepare each later phase plan against the interfaces actually delivered by preceding phases, rather than inventing parallel, incompatible contracts in this index.
- A local-ready identity configuration can unblock local development with an OIDC test provider. It does not mean real Resend, Google, SSO, or logout has passed staging.
- Do not advance a dependent phase through a known failing automated test or incompatible interface.
- Preserve exact test commands and exit results. Record public runtime versions separately from build versions.
- A missing deployment secret is an external input, not permission to invent a secret, use an old exposed value, or configure production automatically.
