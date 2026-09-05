package store

import (
	"context"
	"database/sql"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func (p *ControlPostgres) ResolveAccount(ctx context.Context, issuer, subject string) (domain.Account, error) {
	if issuer == "" || subject == "" {
		return domain.Account{}, domain.ErrInvalidRequest
	}

	var account domain.Account
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		now := p.nowUTC()
		return tx.QueryRowContext(ctx, `INSERT INTO tunnel_accounts
			(identity_issuer, identity_subject, created_at, last_seen_at)
			VALUES ($1,$2,$3,$3)
			ON CONFLICT (identity_issuer, identity_subject) DO UPDATE
			SET last_seen_at = GREATEST(tunnel_accounts.last_seen_at, EXCLUDED.last_seen_at)
			RETURNING id::text, identity_issuer, identity_subject, created_at, last_seen_at`,
			issuer, subject, now).Scan(&account.ID, &account.IdentityIssuer, &account.IdentitySubject, &account.CreatedAt, &account.LastSeenAt)
	})
	if err != nil {
		return domain.Account{}, err
	}
	return account, nil
}
