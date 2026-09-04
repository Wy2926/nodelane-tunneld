# NodeLane Tunnel

NodeLane Tunnel is the thin product layer around an unmodified frp data plane. It
provides anonymous client identities, tunnel-scoped credentials, IP/client
limits, bans, automatic HTTP subdomains and TCP/UDP port allocation.

Public service: `tunnel.nodelane.net`

HTTP tunnels: `http://<slug>.tunnel.nodelane.net`

## User commands

Linux:

```sh
curl -fsSL https://tunnel.nodelane.net/run.sh | sh
```

Windows PowerShell:

```powershell
irm https://tunnel.nodelane.net/run.ps1 | iex
```

Windows CMD:

```bat
curl -fsSL https://tunnel.nodelane.net/run.cmd | cmd
```

The bootstrap installs the `nt` command and opens an interactive form. Use the
arrow keys to choose HTTP, TCP, or UDP. An empty host uses `localhost`; the port
defaults to `3000` and is validated immediately against the `1–65535` range.

After the first run, start another tunnel directly:

```sh
nt http localhost 3000
```

The direct command accepts `protocol host port` for non-interactive use. If any
value is missing, `nt` opens the same form with supplied values prefilled and
editable.

On Windows, both installers persist the command directory in the user PATH;
PowerShell also updates the current session. Linux persists `~/.local/bin` in
the shell startup file. Every bootstrap launches the installed executable
directly, so the first tunnel works without reopening the terminal.

The bootstrap shows a package download progress bar, verifies the SHA-256
checksum and embedded version, and then atomically points the `nt` launcher at
the installed version. Running the bootstrap again upgrades that launcher when
`stable.txt` names a new release; a failed download or validation leaves the
current client unchanged. The immediately previous version is retained for
rollback and older versions are removed. The official frp 0.70.0 client service
is compiled into `nt`; no separate `frpc` executable is downloaded or
extracted. Once connected, the client highlights the public address and prints
the NodeLane banner. HTTP tunnels log each request's time, source IP, method and
address. TCP and UDP tunnels show live active/total connection counts and bytes
received from and sent to the public side. Set `NO_COLOR=1` if ANSI colors are
not wanted.

## Local development

Go 1.27 or newer is required. The website in `web/` uses Astro 7 with pnpm and
requires Node.js 22 or newer.

Build or preview the website:

```powershell
pnpm --dir web install
pnpm --dir web dev
pnpm --dir web build
```

English is served at `/`; 11 additional localized routes are generated from
`web/src/i18n/config.ts`. Shared NodeLane design tokens and site chrome live
in `web/src/styles/design-system.css`; Tunnel-only layouts live in
`web/src/styles/tunnel.css`; each translation lives in its own typed file under
`web/src/i18n/locales/`. Each language has a stable URL, including `/zh-cn/` for
Simplified Chinese, and the language switcher navigates directly between them.

The generated site is embedded from `internal/server/assets/web`. Docker and
`deploy/package.ps1` rebuild and copy it automatically before compiling Go.

```powershell
$env:DEV_MODE = "true"
$env:LISTEN_ADDR = ":3000"
go run ./cmd/tunneld
```

Development mode uses in-memory storage and development-only secrets. It must
never be enabled on the public server.

Run tests and build both programs:

```powershell
go test ./...
go build ./cmd/tunneld
go build ./cmd/nt
```

To test the CLI against a local control server, set:

```powershell
$env:NT_API_URL = "http://127.0.0.1:3000/api/v1"
```

### Client language

`nt` automatically follows the operating-system locale and supports the same
12 languages as the website: Simplified Chinese, Traditional Chinese, English,
Spanish, French, German, Japanese, Korean, Brazilian Portuguese, Russian,
Arabic, and Hindi. Override the language for one invocation with `--lang`:

```powershell
nt --lang en http localhost 3000
nt --lang zh-TW tcp localhost 22
nt languages
```

Set `NT_LANG` to choose a persistent language. On Unix-like systems, `LC_ALL`,
`LC_MESSAGES`, and `LANG` are used when `NT_LANG` is unset. `--lang auto` keeps
the automatic environment and operating-system detection for that invocation.

## Production deployment

### Recommended: private registry image

The release image is multi-architecture, contains tunneld plus all four
single-binary `nt` client bundles, and contains no production secrets.
Authenticate Docker to your registry once, then publish it from Windows:

```powershell
docker login docker.nodelane.net
.\deploy\publish-image.ps1 `
  -Registry docker.nodelane.net `
  -Version 0.3.0 `
  -TagStable
```

Linux and CI can use the equivalent command:

```sh
docker login docker.nodelane.net
sh deploy/publish-image.sh docker.nodelane.net 0.3.0
```

Set the immutable image in `.env`:

```dotenv
TUNNELD_IMAGE=docker.nodelane.net/nodelane/tunneld:0.3.0
PUBLIC_SCHEME=http
```

`PUBLIC_SCHEME` controls the URL returned for HTTP tunnels and defaults to
`http`. Change it to `https` only after wildcard TLS termination is available
for `*.tunnel.nodelane.net`. This setting does not control the embedded frp
client's TLS connection to frps.

The production rollout is only an image pull and Compose update. It does not
run `install.sh` or rebuild source on the server:

```sh
docker compose --env-file .env -f deploy/compose.registry.yaml pull
docker compose --env-file .env -f deploy/compose.registry.yaml up -d --remove-orphans
curl -fsS http://127.0.0.1:9000/healthz
docker compose --env-file .env -f deploy/compose.registry.yaml logs --tail=100 tunneld
```

No database migration is required for this change. An existing `.env` without
`PUBLIC_SCHEME` also defaults to `http`, so pulling and recreating the container
is sufficient for tunneld itself. The external 1Panel/OpenResty/Caddy wildcard
route is not managed by the image and must separately accept port 80 without an
HTTP-to-HTTPS redirect.

In 1Panel, use the same image, host networking, and `.env`. Do not mount an old
host release directory over `/releases`, because the image already carries the
matching client bundles. The image deliberately does not contain `.env`.
`deploy/compose.1panel.yaml` is ready for an `.env` stored at
`/opt/nodelane/tunnel/.env`.

For a registry served over plain HTTP, configure it as an insecure registry in
the Docker daemon on both hosts. Registry credentials and daemon trust settings
are intentionally not accepted by the publish scripts.

### Private bundle fallback

On the development Windows machine (replace the version when publishing a new
release):

```powershell
.\deploy\package.ps1 -Version 0.1.4
```

Upload `dist/nodelane-tunnel-0.1.4-linux.tar.gz` and its `.sha256` file to the
server. The archive already contains Linux server binaries for amd64/arm64,
four single-binary client releases with embedded frp 0.70.0, production
secrets, `.env`, frps config
and the installer. Keep it private because it contains secrets.

On the server:

```sh
sha256sum -c nodelane-tunnel-0.1.4-linux.tar.gz.sha256
mkdir -p /opt/nodelane
tar -xzf nodelane-tunnel-0.1.4-linux.tar.gz -C /opt/nodelane
cd /opt/nodelane/tunnel
# Set DATABASE_URL and REDIS_PASSWORD in .env, then:
sh install.sh
```

The installer selects the server architecture, starts only `tunneld`, publishes
the already-bundled client files, and checks its own health. PostgreSQL and
Redis are external dependencies reached through `127.0.0.1:5432` and
`127.0.0.1:6379`; runtime configuration does not contain their container or
Docker network names. The installer does not inspect or change frps,
PostgreSQL, Redis, 1Panel, OpenResty, firewall rules, DNS, systemd, or unrelated
Docker resources. At startup, `tunneld` idempotently initializes only its own
tables and indexes in the database selected by `DATABASE_URL`; that database
and login role must already exist.

After reviewing the generated `frps.toml`, apply it to frps manually. Then add
these two reverse proxies manually in 1Panel:

- `tunnel.nodelane.net` → `http://127.0.0.1:9000`
- `*.tunnel.nodelane.net` → `http://127.0.0.1:8080` with the original Host
  header preserved; expose this proxy on public port 80 while
  `PUBLIC_SCHEME=http`, without forcing an HTTPS redirect

### Source deployment fallback

1. Copy `.env.example` to `.env` and replace every secret.
2. Use `deploy/frps.toml` as the complete frps 0.70.0 configuration, or merge
   `deploy/frps.additions.toml` into an existing advanced configuration.
3. Merge `deploy/openresty.conf` into the existing OpenResty configuration.
   `deploy/Caddyfile` remains available for installations that use Caddy.
4. Open the configured TCP and UDP public port ranges in the host firewall and
   cloud security group. Keep ports `8080`, `9000`, PostgreSQL and Redis private.
5. From the repository root, start the control plane with
   `docker compose --env-file .env -f deploy/compose.yaml up -d`.
6. Restart frps only after `frps verify -c /path/to/frps.toml` succeeds.
7. Publish release artifacts under `/srv/nodelane/tunnel/releases/<version>` and
   write the active version to `/srv/nodelane/tunnel/releases/stable.txt`.

For a first release directly on the Linux server (Docker required, host Go is
not required), run:

```sh
sh deploy/build-release.sh 0.1.0
```

This builds all four supported single-binary client targets with frp 0.70.0
embedded, writes SHA-256 files and atomically activates the version through
`stable.txt`.

The embedded frp client uses frp's encrypted default TLS transport and does not
require the OpenResty/1Panel HTTPS certificate. Optional server identity
verification can be enabled later by distributing a CA file through
`NT_CA_FILE`.

The provided Compose deployment targets Linux: `tunneld` uses host networking
so 1Panel/OpenResty, frps, PostgreSQL and Redis can all be reached through
stable loopback ports without coupling the application to Docker resource
names. The standard Compose file contains only `tunneld`.

When Cloudflare proxying is enabled, OpenResty must restore `$remote_addr` from
`CF-Connecting-IP` only for Cloudflare source ranges. The supplied
`deploy/openresty.conf` includes the current ranges and forwards the normalized
address as `X-Real-IP`. Keep the origin behind a firewall that accepts HTTPS
from Cloudflare ranges only, and update the ranges from
<https://www.cloudflare.com/ips/> when Cloudflare publishes changes.

## Required frps values

The deployment currently targets stilleshan/frps `0.70.0`, running with host
networking and the default control port `7000`. The generated configuration
uses private HTTP ingress port `8080`, TCP ports `20000-29999`, and UDP ports
`30000-39999`. The public ranges must be allowed by the host firewall and
cloud security group before those tunnel types are usable.

The private bundle generates one random `auth.token` and writes the identical
value to `FRP_AUTH_TOKEN` in `.env`. Keep both files private. frp transport
encryption does not require the public 1Panel certificate or its private key;
1Panel continues to manage public HTTPS independently.

## FRP protocol compatibility and diagnostics

`nt` embeds the official frp `0.70.0` client service and explicitly selects
TCP, TLS, yamux, and wire protocol v1. It uses the official `clientID` field and
requires the `user` field to remain empty because `user` prefixes proxy names.
The client and server also declare the same `HeartBeats` and `NewWorkConns` auth
scopes; omitting them on the client creates a proxy that appears online but
rejects every work connection.

Every HTTP request now has an `X-Request-ID`. FRP callbacks additionally log
the upstream `X-Frp-Reqid`, operation, validation stage, run ID, tunnel ID,
proxy name, decision, status, and duration without logging credentials. Set
`LOG_LEVEL=debug` for callback receipt and heartbeat/work-connection details.
Set `NT_FRP_LOG=1` and optionally `NT_FRP_LOG_LEVEL=trace` on a client to
stream embedded frp diagnostics instead of showing them only when startup
fails.

`yamux: Invalid protocol version: 71` means the first decrypted byte was ASCII
`G`, normally the beginning of an HTTP `GET`. This happens before any HTTP
plugin callback. Check that port 7000 is a raw TCP route to frps, is not an
HTTPS health-check target, and is not reached with `transport.protocol =
"wss"` unless a reverse proxy terminates WSS before frps. The companion
[troubleshooting guide](docs/frp-troubleshooting.md) separates transport and
callback checks.

## Administrative bans

Ban a client:

```sh
curl -X POST http://127.0.0.1:9000/internal/admin/clients/cli_xxx/ban \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"reason":"abuse"}'
```

Ban an IPv4, IPv6 or CIDR range:

```sh
curl -X POST http://127.0.0.1:9000/internal/admin/ip-bans \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"network":"203.0.113.0/24","scope":"tunnel_client","reason":"abuse"}'
```

The example Caddyfile intentionally does not expose `/internal/*`. Invoke these
administrative endpoints through loopback or a private management network.
