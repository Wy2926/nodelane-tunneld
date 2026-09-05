package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func (p *ControlPostgres) ResolveAccount(ctx context.Context, issuer, subject string) (domain.Account, error) {
	if issuer == "" || subject == "" {
		return domain.Account{}, domain.ErrInvalidRequest
	}

	var account domain.Account
	err := p.withControlTx(ctx, func(tx *sql.Tx) error {
		var candidateID, accountID string
		if err := tx.QueryRowContext(ctx, `SELECT gen_random_uuid()::text`).Scan(&candidateID); err != nil {
			return err
		}
		provisional := time.Unix(0, 0).UTC()
		if err := tx.QueryRowContext(ctx, `INSERT INTO tunnel_accounts
				(id, identity_issuer, identity_subject, created_at, last_seen_at)
				VALUES ($1,$2,$3,$4,$4)
				ON CONFLICT (identity_issuer, identity_subject) DO UPDATE
				SET last_seen_at = tunnel_accounts.last_seen_at
				RETURNING id::text`,
			candidateID, issuer, subject, provisional).Scan(&accountID); err != nil {
			return err
		}
		now := p.nowUTC()
		return tx.QueryRowContext(ctx, `UPDATE tunnel_accounts
			SET created_at = CASE WHEN id=$2 THEN $3 ELSE created_at END,
				last_seen_at = GREATEST(last_seen_at, $3)
			WHERE id=$1
			RETURNING id::text, identity_issuer, identity_subject, created_at, last_seen_at`,
			accountID, candidateID, now).Scan(&account.ID, &account.IdentityIssuer, &account.IdentitySubject, &account.CreatedAt, &account.LastSeenAt)
	})
	if err != nil {
		return domain.Account{}, err
	}
	return account, nil
}
