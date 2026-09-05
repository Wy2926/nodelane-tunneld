package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func (p *ControlPostgres) withControlTx(ctx context.Context, fn func(*sql.Tx) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = fn(tx)
		if err != nil {
			_ = tx.Rollback()
			if attempt < 2 && retryableControlTxError(err) && ctx.Err() == nil {
				continue
			}
			return err
		}
		return tx.Commit()
	}
	return errors.New("control transaction retry limit reached")
}

func retryableControlTxError(err error) bool {
	var state interface{ SQLState() string }
	if !errors.As(err, &state) {
		return false
	}
	return state.SQLState() == "40001" || state.SQLState() == "40P01"
}

func lockControlAccounts(ctx context.Context, tx *sql.Tx, ids ...string) error {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	last := ""
	haveLast := false
	for _, id := range sorted {
		if haveLast && id == last {
			continue
		}
		if id == "" {
			return domain.ErrNotFound
		}
		last = id
		haveLast = true
		var locked string
		if err := tx.QueryRowContext(ctx, `SELECT id::text FROM tunnel_accounts WHERE id = $1 FOR UPDATE`, id).Scan(&locked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
	}
	return nil
}
