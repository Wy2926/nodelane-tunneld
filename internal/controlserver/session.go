package controlserver

import (
	"context"
	"errors"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

type sessionRefresher struct {
	*session.RedisStore
	provider interface {
		Refresh(context.Context, identity.OIDCTokens) (identity.OIDCTokens, error)
	}
	now func() time.Time
}

func (s *sessionRefresher) ReadSession(ctx context.Context, id string) (session.Record, error) {
	record, err := s.RedisStore.ReadSession(ctx, id)
	if err != nil {
		return session.Record{}, err
	}
	if record.Tokens.AccessTokenExpiresAt.After(s.now().Add(30 * time.Second)) {
		return record, nil
	}
	lease, err := s.AcquireRefresh(ctx, id, record.Version, 15*time.Second)
	if errors.Is(err, session.ErrConflict) {
		current, readErr := s.RedisStore.ReadSession(ctx, id)
		if readErr == nil && current.Version > record.Version && current.Tokens.AccessTokenExpiresAt.After(s.now()) {
			return current, nil
		}
		return session.Record{}, session.ErrUnavailable
	}
	if err != nil {
		return session.Record{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.ReleaseRefresh(cleanup, lease)
	}()
	refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tokens, err := s.provider.Refresh(refreshCtx, record.Tokens)
	if errors.Is(err, identity.ErrOIDCUnauthorized) {
		return session.Record{}, session.ErrExpired
	}
	if err != nil {
		return session.Record{}, session.ErrUnavailable
	}
	return s.CommitRefresh(refreshCtx, lease, tokens)
}
