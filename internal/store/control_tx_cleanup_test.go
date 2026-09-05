package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestControlTransactionRollsBackWhenCallbackPanics(t *testing.T) {
	store, db := newControlTestStore(t)
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)
	account, _ := seedControlAccountRoute(t, db)

	const panicValue = "control transaction callback panic"
	var retainedTx *sql.Tx
	t.Cleanup(func() {
		if retainedTx != nil {
			_ = retainedTx.Rollback()
		}
	})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = store.withControlTx(context.Background(), func(tx *sql.Tx) error {
			retainedTx = tx
			if _, err := tx.Exec(`UPDATE tunnel_accounts SET identity_subject = 'changed-in-panicking-transaction' WHERE id = $1`, account.ID); err != nil {
				t.Fatal(err)
			}
			panic(panicValue)
		})
	}()
	if recovered != panicValue {
		t.Fatalf("recovered panic = %#v, want %q", recovered, panicValue)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var subject string
	err := store.withControlTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT identity_subject FROM tunnel_accounts WHERE id = $1`, account.ID).Scan(&subject)
	})
	if err != nil {
		t.Errorf("subsequent transaction could not obtain the only connection: %v", err)
	} else if subject != "subject-1" {
		t.Errorf("subject after panic = %q, want %q", subject, "subject-1")
	}

	persistedCtx, persistedCancel := context.WithTimeout(context.Background(), time.Second)
	defer persistedCancel()
	if err := db.QueryRowContext(persistedCtx, `SELECT identity_subject FROM tunnel_accounts WHERE id = $1`, account.ID).Scan(&subject); err != nil {
		t.Fatalf("read persisted account state: %v", err)
	}
	if subject != "subject-1" {
		t.Fatalf("persisted subject after panic = %q, want %q", subject, "subject-1")
	}
}
