NodeLane Tunnel server bundle
=============================

install.sh only:
  - detects Linux amd64 or arm64;
  - selects the bundled tunneld binary inside this directory;
  - starts/updates the Docker Compose project "nodelane-tunnel";
  - checks tunneld at http://127.0.0.1:9000/healthz.

The Compose project contains only tunneld. It does not create PostgreSQL or
Redis containers or volumes. At runtime tunneld connects to the stable host
loopback ports in .env, so it does not depend on 1Panel container names or
Docker network names.

Before the first installation:
  1. Set DATABASE_URL in .env to an existing PostgreSQL database.
  2. Set REDIS_PASSWORD in .env to the existing Redis password. Use an
     empty value only if Redis authentication is disabled.

It does not inspect or change frps, 1Panel, OpenResty, PostgreSQL, Redis,
firewall rules, DNS, systemd, or unrelated Docker containers and volumes.

Files requiring manual review:
  frps.toml  Complete frps 0.70.0 configuration. It is never auto-applied.
  DEPLOY.txt Deployment summary and the two manual 1Panel proxy targets.

The installer does not manage PostgreSQL itself. On first startup, tunneld
initializes its own tables and indexes inside the database selected by
DATABASE_URL. The role and database must already exist and permit schema
creation. Initialization is idempotent (CREATE ... IF NOT EXISTS) and never
drops tables. Redis keys use the "nodelane:tunnel:" prefix to avoid collisions.

Useful commands (run from this directory):
  docker compose --project-name nodelane-tunnel --env-file .env -f compose.yaml ps
  docker compose --project-name nodelane-tunnel --env-file .env -f compose.yaml logs -f
  docker compose --project-name nodelane-tunnel --env-file .env -f compose.yaml down

The final command removes only this project's tunneld container. This Compose
file owns no PostgreSQL or Redis containers, networks, or data volumes.
