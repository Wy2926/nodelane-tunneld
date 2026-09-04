package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schema string

type Postgres struct {
	db *sql.DB
}

func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}
	return &Postgres{db: db}, nil
}

func (p *Postgres) CreateClient(ctx context.Context, client domain.Client, token domain.ClientToken) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO clients (id, account_id, status, ban_reason, banned_until, registration_ip, last_ip, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6::inet, $7::inet, $8, $9)`,
		client.ID, client.AccountID, client.Status, nullString(client.BanReason), client.BannedUntil,
		client.RegistrationIP.String(), client.LastIP.String(), client.CreatedAt, client.LastSeenAt)
	if err != nil {
		return mapWriteError(err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO client_tokens (id, client_id, token_hash, created_at, last_used_at, expires_at, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, token.ID, token.ClientID, token.TokenHash,
		token.CreatedAt, token.LastUsedAt, token.ExpiresAt, token.RevokedAt)
	if err != nil {
		return mapWriteError(err)
	}
	return tx.Commit()
}

func (p *Postgres) GetClient(ctx context.Context, id string) (domain.Client, error) {
	var client domain.Client
	var accountID, banReason sql.NullString
	var registrationIP, lastIP string
	row := p.db.QueryRowContext(ctx, `
		SELECT id, account_id::text, status, ban_reason, banned_until,
		       host(registration_ip), host(last_ip), created_at, last_seen_at
		FROM clients WHERE id = $1`, id)
	err := row.Scan(&client.ID, &accountID, &client.Status, &banReason, &client.BannedUntil,
		&registrationIP, &lastIP, &client.CreatedAt, &client.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Client{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Client{}, err
	}
	if accountID.Valid {
		client.AccountID = &accountID.String
	}
	client.BanReason = banReason.String
	client.RegistrationIP, _ = netip.ParseAddr(registrationIP)
	client.LastIP, _ = netip.ParseAddr(lastIP)
	return client, nil
}

func (p *Postgres) GetClientToken(ctx context.Context, id string) (domain.ClientToken, error) {
	var token domain.ClientToken
	err := p.db.QueryRowContext(ctx, `
		SELECT id, client_id, token_hash, created_at, last_used_at, expires_at, revoked_at
		FROM client_tokens WHERE id = $1`, id).Scan(&token.ID, &token.ClientID, &token.TokenHash,
		&token.CreatedAt, &token.LastUsedAt, &token.ExpiresAt, &token.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ClientToken{}, domain.ErrNotFound
	}
	return token, err
}

func (p *Postgres) TouchClientToken(ctx context.Context, id string, at time.Time) error {
	result, err := p.db.ExecContext(ctx, `UPDATE client_tokens SET last_used_at = $2 WHERE id = $1`, id, at)
	return requireRow(result, err)
}

func (p *Postgres) TouchClient(ctx context.Context, id string, ip netip.Addr, at time.Time) error {
	result, err := p.db.ExecContext(ctx, `UPDATE clients SET last_ip = $2::inet, last_seen_at = $3 WHERE id = $1`, id, ip.String(), at)
	return requireRow(result, err)
}

func (p *Postgres) BanClient(ctx context.Context, id, reason string, until *time.Time) error {
	result, err := p.db.ExecContext(ctx, `
		UPDATE clients SET status = 'banned', ban_reason = $2, banned_until = $3 WHERE id = $1`, id, reason, until)
	return requireRow(result, err)
}

func (p *Postgres) CreateTunnel(ctx context.Context, tunnel domain.Tunnel) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE tunnels SET status = 'expired', closed_at = $1
		WHERE status IN ('reserved', 'online') AND expires_at <= $1`, tunnel.CreatedAt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tunnels
		(id, client_id, token_id, protocol, node_id, proxy_name, subdomain, remote_port,
		 request_ip, connected_ip, status, created_at, connected_at, expires_at, closed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::inet, NULL, $10, $11, NULL, $12, NULL)`,
		tunnel.ID, tunnel.ClientID, tunnel.TokenID, tunnel.Protocol, tunnel.NodeID, tunnel.ProxyName,
		nullString(tunnel.Subdomain), nullInt(tunnel.RemotePort), tunnel.RequestIP.String(), tunnel.Status,
		tunnel.CreatedAt, tunnel.ExpiresAt)
	if err != nil {
		return mapWriteError(err)
	}
	return tx.Commit()
}

func (p *Postgres) GetTunnel(ctx context.Context, id string) (domain.Tunnel, error) {
	var tunnel domain.Tunnel
	var subdomain, requestIP, connectedIP sql.NullString
	var remotePort sql.NullInt64
	row := p.db.QueryRowContext(ctx, `
		SELECT id, client_id, token_id, protocol, node_id, proxy_name, subdomain, remote_port,
		       host(request_ip), host(connected_ip), status, created_at, connected_at, expires_at, closed_at
		FROM tunnels WHERE id = $1`, id)
	err := row.Scan(&tunnel.ID, &tunnel.ClientID, &tunnel.TokenID, &tunnel.Protocol, &tunnel.NodeID,
		&tunnel.ProxyName, &subdomain, &remotePort, &requestIP, &connectedIP, &tunnel.Status,
		&tunnel.CreatedAt, &tunnel.ConnectedAt, &tunnel.ExpiresAt, &tunnel.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Tunnel{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Tunnel{}, err
	}
	tunnel.Subdomain = subdomain.String
	tunnel.RemotePort = int(remotePort.Int64)
	if requestIP.Valid {
		tunnel.RequestIP, _ = netip.ParseAddr(requestIP.String)
	}
	if connectedIP.Valid {
		tunnel.ConnectedIP, _ = netip.ParseAddr(connectedIP.String)
	}
	return tunnel, nil
}

func (p *Postgres) UpdateTunnelConnected(ctx context.Context, id string, ip netip.Addr, at time.Time) error {
	result, err := p.db.ExecContext(ctx, `
		UPDATE tunnels SET status = 'online', connected_ip = $2::inet, connected_at = COALESCE(connected_at, $3)
		WHERE id = $1 AND status IN ('reserved', 'online')`, id, ip.String(), at)
	return requireRow(result, err)
}

func (p *Postgres) CloseTunnel(ctx context.Context, id string, status domain.TunnelStatus, at time.Time) error {
	result, err := p.db.ExecContext(ctx, `UPDATE tunnels SET status = $2, closed_at = $3 WHERE id = $1`, id, status, at)
	return requireRow(result, err)
}

func (p *Postgres) IsIPBanned(ctx context.Context, ip netip.Addr, scope string, now time.Time) (bool, error) {
	var banned bool
	err := p.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM network_bans
			WHERE $1::inet <<= network
			  AND (scope = 'both' OR scope = $2)
			  AND (expires_at IS NULL OR expires_at > $3)
		)`, ip.String(), scope, now).Scan(&banned)
	return banned, err
}

func (p *Postgres) CreateNetworkBan(ctx context.Context, ban domain.NetworkBan) error {
	_, err := p.db.ExecContext(ctx, `
		INSERT INTO network_bans (id, network, scope, reason, expires_at, created_at)
		VALUES ($1, $2::cidr, $3, $4, $5, $6)`, ban.ID, ban.Network.String(), ban.Scope, ban.Reason, ban.ExpiresAt, ban.CreatedAt)
	return mapWriteError(err)
}

func (p *Postgres) Close() error { return p.db.Close() }

func requireRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func mapWriteError(err error) error {
	if err == nil {
		return nil
	}
	if state, ok := err.(interface{ SQLState() string }); ok && state.SQLState() == "23505" {
		return domain.ErrConflict
	}
	return err
}
