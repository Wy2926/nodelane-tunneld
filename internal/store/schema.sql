CREATE TABLE IF NOT EXISTS clients (
    id              TEXT PRIMARY KEY,
    account_id      UUID NULL,
    status          TEXT NOT NULL CHECK (status IN ('active', 'limited', 'banned')),
    ban_reason      TEXT NULL,
    banned_until    TIMESTAMPTZ NULL,
    registration_ip INET NOT NULL,
    last_ip         INET NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    last_seen_at    TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS client_tokens (
    id           TEXT PRIMARY KEY,
    client_id    TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ NULL,
    expires_at   TIMESTAMPTZ NULL,
    revoked_at   TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS client_tokens_client_idx ON client_tokens (client_id);

CREATE TABLE IF NOT EXISTS tunnels (
    id           TEXT PRIMARY KEY,
    client_id    TEXT NOT NULL REFERENCES clients(id),
    token_id     TEXT NOT NULL UNIQUE,
    protocol     TEXT NOT NULL CHECK (protocol IN ('http', 'tcp', 'udp')),
    node_id      TEXT NOT NULL,
    proxy_name   TEXT NOT NULL UNIQUE,
    subdomain    TEXT NULL,
    remote_port  INTEGER NULL,
    request_ip   INET NOT NULL,
    connected_ip INET NULL,
    status       TEXT NOT NULL CHECK (status IN ('reserved', 'online', 'closed', 'expired')),
    created_at   TIMESTAMPTZ NOT NULL,
    connected_at TIMESTAMPTZ NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    closed_at    TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS tunnels_active_subdomain_idx
    ON tunnels (subdomain)
    WHERE subdomain IS NOT NULL AND status IN ('reserved', 'online');

CREATE UNIQUE INDEX IF NOT EXISTS tunnels_active_remote_port_idx
    ON tunnels (protocol, remote_port)
    WHERE remote_port IS NOT NULL AND status IN ('reserved', 'online');

CREATE INDEX IF NOT EXISTS tunnels_client_created_idx ON tunnels (client_id, created_at DESC);
CREATE INDEX IF NOT EXISTS tunnels_expires_idx ON tunnels (expires_at) WHERE status IN ('reserved', 'online');

CREATE TABLE IF NOT EXISTS network_bans (
    id         TEXT PRIMARY KEY,
    network    CIDR NOT NULL,
    scope      TEXT NOT NULL CHECK (scope IN ('tunnel_client', 'public_visitor', 'both')),
    reason     TEXT NOT NULL,
    expires_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS network_bans_network_idx ON network_bans USING GIST (network inet_ops);
