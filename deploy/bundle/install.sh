#!/bin/sh
set -eu

# Scope: this script only prepares files in this extracted directory and
# creates/updates the Docker Compose project named "nodelane-tunnel".
# It never reads, edits, stops, restarts or replaces frps, PostgreSQL, Redis,
# 1Panel, OpenResty, firewall rules, DNS, systemd, or unrelated containers.

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this installer as root." >&2
  exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required." >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required." >&2
  exit 1
fi
if command -v curl >/dev/null 2>&1; then
  health_command="curl -fsS http://127.0.0.1:9000/healthz"
elif command -v wget >/dev/null 2>&1; then
  health_command="wget -qO- http://127.0.0.1:9000/healthz"
else
  echo "curl or wget is required for the local health check." >&2
  exit 1
fi

bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$bundle_dir"

if grep -q '^DATABASE_URL=REPLACE_WITH_1PANEL_POSTGRES_URL$' .env; then
  echo "Set DATABASE_URL in $bundle_dir/.env before installation." >&2
  exit 1
fi
if grep -q '^REDIS_PASSWORD=REPLACE_WITH_1PANEL_REDIS_PASSWORD$' .env; then
  echo "Set REDIS_PASSWORD in $bundle_dir/.env before installation." >&2
  echo "Use REDIS_PASSWORD= only if authentication is disabled." >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) server_arch=amd64 ;;
  aarch64|arm64) server_arch=arm64 ;;
  *) echo "Unsupported server architecture: $(uname -m)" >&2; exit 1 ;;
esac

# Select the already bundled server binary. Nothing is installed globally.
cp "bin/tunneld-linux-$server_arch" bin/tunneld
chmod 755 bin/tunneld
chmod 600 .env frps.toml
find releases -type d -exec chmod 755 {} \;
find releases -type f -exec chmod 644 {} \;

docker compose \
  --project-name nodelane-tunnel \
  --env-file .env \
  -f compose.yaml \
  up -d --build

healthy=false
attempt=0
while [ "$attempt" -lt 30 ]; do
  if sh -c "$health_command" >/dev/null 2>&1; then
    healthy=true
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done

if [ "$healthy" != true ]; then
  docker compose \
    --project-name nodelane-tunnel \
    --env-file .env \
    -f compose.yaml \
    logs --tail=100 tunneld
  echo "tunneld did not become healthy. No external service was changed." >&2
  exit 1
fi

echo
echo "NodeLane Tunnel is running."
echo "Architecture: $server_arch"
echo "Health: http://127.0.0.1:9000/healthz"
echo "PostgreSQL: existing service at 127.0.0.1:5432"
echo "Redis: existing service at 127.0.0.1:6379"
echo
echo "No frps, PostgreSQL, Redis, 1Panel, OpenResty, firewall or DNS setting was changed."
echo "Review and apply manually when ready: $bundle_dir/frps.toml"
echo "Manual 1Panel reverse proxies:"
echo "  tunnel.nodelane.net   -> http://127.0.0.1:9000"
echo "  *.tunnel.nodelane.net -> http://127.0.0.1:8080 (port 80, preserve Host, no HTTPS redirect)"
