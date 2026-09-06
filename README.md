# NodeLane Tunnel

A Go control plane, static Astro console, and `nt` CLI using unmodified
[frp 0.70.0](https://github.com/fatedier/frp). The console owns permanent
HTTP routes; anonymous HTTP, TCP, and UDP runs use Redis only.

This is a breaking-version codebase. Use a new Tunnel PostgreSQL database
and Redis namespace. There is no old API, data import, dual write, or upgrade
compatibility. Startup refuses an incompatible schema and never deletes it.

## Use

The Tunnel console is served at `/console/tunnels`. It supports route
creation, single-use launch commands, current traffic snapshots, stopping,
deletion, and recovery. The safety limit is five active routes per account;
deleted names stay reserved for seven days and until their old entry is released.

After installing the matching client:

```sh
nt
nt anonymous http localhost 3000
nt anonymous tcp localhost 22
nt anonymous udp localhost 5353
nt login
nt routes
nt start demo localhost 3000
nt launch <single-use-code> localhost 3000
nt logout
```

`nt` offers anonymous use or account login. `launch` never opens the account
credential store. Every run performs a local target check before requesting
resources. Ctrl+C closes the local client and reports the stop with its run
credential. HTTP routes publish `http://<name>.<PUBLIC_DOMAIN>`; HTTPS tunnels
are not offered.

The website generates distinct Linux, PowerShell, and CMD commands with
validated arguments. Parameterized CMD installation uses an exclusively created
temporary directory; `run.cmd | cmd` is only the parameterless entry.
Downloaded packages are SHA-256 checked before execution.

## Credentials And Transport

- Logto provides Device Flow and browser OIDC; no custom identity protocol.
- Refresh tokens use the system credential store. Linux file storage is an
  explicit lower-security fallback with owner-only permissions.
- Account tokens go only to the Tunnel API. Run credentials stay in memory:
  Linux sealed memfd or a Windows current-user/current-process named pipe.
- Official frpc TokenSource carries an independent run proof on Login, Ping,
  and NewWorkConn. The plugin validates current authority before converting
  that proof to the empty native-token checksum. Inherited session metadata
  alone never authorizes a new work connection.
- frps enforces TLS and clients verify its CA and server name. Do not configure
  a shared `FRP_AUTH_TOKEN`.
- Native session IDs change on every Login; business run IDs and URLs do not.
- Missing recovery evidence keeps resources occupied. Entry reuse does not
  assert that every pre-existing stream has finished.

The console shows native current connections and today's upload/download
bytes in UTC. There are no historical graphs, traffic/request logs, Prometheus
services, HTTP visitor-concurrency caps, billing, or byte quotas.

## Configuration

See [.env.example](.env.example) for required names. Supply real values privately:
separate PostgreSQL/Redis access, seven distinct 32-byte secrets, separate Web
and Native Logto application IDs, the Web secret, frps management credentials,
and TLS certificates.

Client options:

| Variable | Purpose |
| --- | --- |
| `NT_API_URL` | API base; default `https://tunnel.nodelane.net/api/v1` |
| `NT_LANG` | One of 12 supported locales; `--lang` takes precedence |
| `NT_ACCOUNT_STORE` | `keyring` (default), or explicit `file` on Linux |
| `NT_ACCOUNT_CREDENTIALS_FILE` | Owner-only file path for the Linux fallback |
| `NT_INSTALLATION_FILE` | Separate anonymous installation ID path |
| `NT_CA_FILE` | Optional trusted frps CA override |
| `NO_COLOR` | Disable terminal styling |

Nonempty `NT_FRP_PROXY_URL` is rejected before allocation because the pinned
upstream proxy negotiation cannot be reliably canceled.

## Self-Hosting

[deploy/frps.toml](deploy/frps.toml) is the complete stock frps template.
The service preflights the same configuration file: TLS, certificate identity,
all six plugin callbacks, finite heartbeats, and private management settings.
Mount public certificates at matching absolute paths in both containers;
mount the private key only in frps.

The Compose examples use Linux host networking. The application, plugin,
and management listeners bind loopback; the reverse proxy exposes only the
intended application and tunnel entrypoints. Do not expose the management API,
plugin listener, PostgreSQL, or Redis publicly.

Validate configuration without starting services:

```sh
docker compose --env-file .env -f deploy/compose.yaml config --quiet
docker compose --env-file .env -f deploy/compose.yaml -f deploy/compose.registry.yaml config --quiet
```

During a clean maintenance window, after confirming the new frps data plane
has no old clients or anonymous proxies, explicitly initialize only a fresh
anonymous namespace:

```sh
tunneld anonymous-resources init --confirm-clean-data-plane
```

The command refuses populated or previously initialized state, checks native
inventory, and uses a Redis process/generation fence. It never flushes data or
restarts frps. Normal service startup does not silently enable the namespace.

Real identity-provider setup and public rollout require separate environment
configuration and acceptance. Building this repository does not publish new
client assets or deploy the hosted service. `client-version.txt` versions
the CLI independently of the control-plane image.

## Development

```sh
go test ./... -count=1
go vet ./...
go build ./cmd/tunneld ./cmd/nt
pnpm --dir web install --frozen-lockfile
pnpm --dir web test
pnpm --dir web test:built
```

PostgreSQL/Redis integration tests require explicit
`NODELANE_TEST_DATABASE_URL` and `NODELANE_TEST_REDIS_URL`. Their guards verify
the local fixture identity before creating isolated test schemas/namespaces;
they never fall back to production environment variables.

The Go binary embeds committed `internal/server/assets/web` output from
`web/dist`; rebuild and synchronize it after frontend changes. Console HTML
is read only after Session authorization; internal shell URLs are not public.

Core boundaries are `controlserver` (composition), `controlapi` / `bff`
(HTTP), `store` / `anonymous` (authority), `frpauth` / `frpanonymous`
(plugin authorization), `frpevidence` / `runtimestats` (native observations),
and `cliauth` / `runclient` / `runsecret` (CLI dependencies).

## Security And License

Report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/Wy2926/nodelane-tunneld/security/advisories/new).
Never include credentials, private connection strings, or sensitive commands.
This software is intended for development and controlled testing, not a
production SLA.

[MIT License](LICENSE). Client packages retain upstream license notices.
