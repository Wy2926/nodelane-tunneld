package cliauth

import (
	"context"
	"errors"
	"time"
)

// LogoutStored resolves the saved identity under the same cross-process lock
// as revocation and cleanup. Damaged identity fields never prevent local logout.
func LogoutStored(ctx context.Context, store Store) error {
	transactional, ok := store.(TransactionalStore)
	if !ok || transactional == nil {
		return ErrInvalidConfiguration
	}
	lockCtx, cancelLock := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelLock()
	return transactional.Transaction(lockCtx, func(locked Store) error {
		loadCtx, cancelLoad := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		credentials, loadErr := locked.Load(loadCtx)
		cancelLoad()
		if loadErr == nil {
			client, err := New(ctx, Options{Issuer: credentials.Issuer, ClientID: credentials.ClientID, Resource: credentials.Resource, Store: transactional})
			if err == nil {
				return client.logout(ctx, locked)
			}
		}
		deleteCtx, cancelDelete := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancelDelete()
		deleteErr := locked.Delete(deleteCtx)
		if deleteErr != nil && !errors.Is(deleteErr, ErrNoCredentials) {
			return errors.Join(ErrCredentialsUnavailable, ErrRevocationUnconfirmed)
		}
		if errors.Is(loadErr, ErrNoCredentials) {
			return nil
		}
		return ErrRevocationUnconfirmed
	})
}
