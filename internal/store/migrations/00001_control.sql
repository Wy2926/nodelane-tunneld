-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = current_schema()
          AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S', 'c')
          AND c.relname NOT IN ('goose_db_version', 'goose_db_version_id_seq')
    ) OR EXISTS (
        SELECT 1
        FROM pg_catalog.pg_proc p
        JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = current_schema()
    ) OR EXISTS (
        SELECT 1
        FROM pg_catalog.pg_type t
        JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname = current_schema()
          AND t.typtype IN ('d', 'e')
    ) THEN
        RAISE EXCEPTION 'control migration requires a fresh schema';
    END IF;
END
$$;
-- +goose StatementEnd

CREATE TABLE tunnel_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_issuer TEXT NOT NULL CHECK (identity_issuer <> ''),
    identity_subject TEXT NOT NULL CHECK (identity_subject <> ''),
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL CHECK (last_seen_at >= created_at),
    UNIQUE (identity_issuer, identity_subject)
);

CREATE TABLE tunnel_routes (
    id TEXT PRIMARY KEY CHECK (id ~ '^rte_[a-z2-7]{26}$'),
    account_id UUID NOT NULL REFERENCES tunnel_accounts(id),
    protocol TEXT NOT NULL CHECK (protocol = 'http'),
    subdomain TEXT NOT NULL CHECK (
        subdomain = lower(subdomain)
        AND subdomain ~ '^[a-z0-9][a-z0-9-]{1,30}[a-z0-9]$'
        AND subdomain NOT LIKE 'xn--%'
        AND subdomain NOT LIKE 'anon-%'
        AND subdomain NOT IN ('www','auth','api','admin','console','status','support','mail','smtp','frp','tunnel')
    ),
    proxy_name TEXT NOT NULL UNIQUE CHECK (proxy_name = id),
    status TEXT NOT NULL CONSTRAINT control_routes_status_check CHECK (status IN ('active', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL CHECK (updated_at >= created_at),
    deleted_at TIMESTAMPTZ NULL,
    recoverable_until TIMESTAMPTZ NULL,
    name_released_at TIMESTAMPTZ NULL,
    CHECK (
        (status = 'active' AND deleted_at IS NULL AND recoverable_until IS NULL AND name_released_at IS NULL)
        OR
        (status = 'deleted' AND deleted_at IS NOT NULL AND recoverable_until IS NOT NULL
            AND recoverable_until >= deleted_at
            AND (name_released_at IS NULL OR name_released_at >= recoverable_until))
    )
);

CREATE UNIQUE INDEX control_routes_unreleased_name_uq
    ON tunnel_routes (lower(subdomain)) WHERE name_released_at IS NULL;
CREATE INDEX control_routes_account_idx ON tunnel_routes (account_id, created_at DESC);
CREATE INDEX control_routes_recovery_idx ON tunnel_routes (recoverable_until) WHERE name_released_at IS NULL;

CREATE TABLE route_launch_codes (
    id TEXT PRIMARY KEY CHECK (id ~ '^nlc_[a-z2-7]{26}$'),
    route_id TEXT NOT NULL REFERENCES tunnel_routes(id),
    secret_hash TEXT NOT NULL CHECK (secret_hash ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at),
    redeemed_at TIMESTAMPTZ NULL CHECK (redeemed_at IS NULL OR redeemed_at >= created_at),
    revoked_at TIMESTAMPTZ NULL CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX control_launch_codes_route_idx ON route_launch_codes (route_id, created_at DESC);
CREATE INDEX control_launch_codes_expiry_idx ON route_launch_codes (expires_at) WHERE redeemed_at IS NULL AND revoked_at IS NULL;

CREATE TABLE tunnel_runs (
    id TEXT PRIMARY KEY CHECK (id ~ '^run_[a-z2-7]{26}$'),
    route_id TEXT NOT NULL REFERENCES tunnel_routes(id),
    started_via TEXT NOT NULL CHECK (started_via IN ('device_login', 'launch_code')),
    status TEXT NOT NULL CHECK (status IN ('starting', 'online', 'stopping', 'offline')),
    desired_state TEXT NOT NULL CHECK (desired_state IN ('running', 'stopped')),
    request_ip INET NOT NULL,
    connected_ip INET NULL,
    created_at TIMESTAMPTZ NOT NULL,
    connected_at TIMESTAMPTZ NULL CHECK (connected_at IS NULL OR connected_at >= created_at),
    last_heartbeat_at TIMESTAMPTZ NULL CHECK (last_heartbeat_at IS NULL OR last_heartbeat_at >= created_at),
    stop_requested_at TIMESTAMPTZ NULL CHECK (stop_requested_at IS NULL OR stop_requested_at >= created_at),
    stopped_at TIMESTAMPTZ NULL CHECK (stopped_at IS NULL OR stopped_at >= created_at),
    connect_deadline_at TIMESTAMPTZ NOT NULL CHECK (connect_deadline_at > created_at),
    lease_expires_at TIMESTAMPTZ NULL CHECK (lease_expires_at IS NULL OR lease_expires_at > created_at),
    stop_reason TEXT NULL,
    proxy_registration_granted BOOLEAN NOT NULL DEFAULT FALSE,
    reconciliation_claimed_at TIMESTAMPTZ NULL,
    CHECK (connected_at IS NOT NULL OR connected_ip IS NULL),
    CHECK (status <> 'online' OR (connected_at IS NOT NULL AND connected_ip IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CHECK (status <> 'offline' OR stopped_at IS NOT NULL),
    CHECK (desired_state <> 'stopped' OR stop_requested_at IS NOT NULL OR stopped_at IS NOT NULL)
);

CREATE UNIQUE INDEX control_runs_active_route_uq
    ON tunnel_runs (route_id) WHERE status IN ('starting', 'online', 'stopping');
CREATE INDEX control_runs_route_idx ON tunnel_runs (route_id, created_at DESC);
CREATE INDEX control_runs_connect_expiry_idx ON tunnel_runs (connect_deadline_at) WHERE status = 'starting';
CREATE INDEX control_runs_lease_expiry_idx ON tunnel_runs (lease_expires_at) WHERE status IN ('online', 'stopping');

CREATE TABLE run_credentials (
    id TEXT PRIMARY KEY CHECK (id ~ '^nrc_[a-z2-7]{26}$'),
    run_id TEXT NOT NULL UNIQUE REFERENCES tunnel_runs(id),
    secret_hash TEXT NOT NULL CHECK (secret_hash ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NULL CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE TABLE operation_replays (
    id TEXT PRIMARY KEY CHECK (id ~ '^rpl_[a-z2-7]{26}$'),
    operation TEXT NOT NULL CHECK (operation IN ('create_route', 'start_run', 'redeem_launch')),
    principal_key TEXT NOT NULL CHECK (principal_key <> '' AND octet_length(principal_key) <= 256),
    key_hash TEXT NOT NULL CHECK (key_hash ~ '^[0-9a-f]{64}$'),
    request_hash TEXT NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    route_id TEXT NULL REFERENCES tunnel_routes(id),
    run_id TEXT NULL REFERENCES tunnel_runs(id),
    response_ciphertext BYTEA NOT NULL CHECK (octet_length(response_ciphertext) > 0),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX control_replay_key_uq
    ON operation_replays (operation, principal_key, key_hash);
CREATE INDEX control_replays_route_idx ON operation_replays (route_id) WHERE route_id IS NOT NULL;
CREATE INDEX control_replays_run_idx ON operation_replays (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX control_replays_expiry_idx ON operation_replays (expires_at);

CREATE TABLE network_bans (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    network CIDR NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('tunnel_client', 'public_visitor', 'both')),
    reason TEXT NOT NULL CHECK (reason <> ''),
    expires_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (expires_at IS NULL OR expires_at > created_at)
);

CREATE INDEX control_network_bans_network_idx ON network_bans USING GIST (network inet_ops);
CREATE INDEX control_network_bans_expiry_idx ON network_bans (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'control schema downgrade requires manual restore';
END
$$;
-- +goose StatementEnd
