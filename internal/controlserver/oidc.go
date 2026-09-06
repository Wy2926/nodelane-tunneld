package controlserver

import (
	"context"
	"sync"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
)

// Discovery is deferred so an OIDC outage cannot disable anonymous service.
// Only a successfully validated provider is retained; requests still verify
// tokens or refresh server-side sessions through the existing OIDC client.
type lazyOIDCProvider struct {
	lifetime    context.Context
	options     identity.OIDCOptions
	mu          sync.Mutex
	client      *identity.OIDCClient
	discovering chan struct{}
}

func (p *lazyOIDCProvider) get(ctx context.Context) (*identity.OIDCClient, error) {
	p.mu.Lock()
	if p.client != nil {
		client := p.client
		p.mu.Unlock()
		return client, nil
	}
	if pending := p.discovering; pending != nil {
		p.mu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
			return nil, identity.ErrOIDCUnavailable
		}
		p.mu.Lock()
		client := p.client
		p.mu.Unlock()
		if client == nil {
			return nil, identity.ErrOIDCUnavailable
		}
		return client, nil
	}
	p.discovering = make(chan struct{})
	p.mu.Unlock()
	client, err := identity.NewOIDCClient(ctx, p.options)
	p.mu.Lock()
	if err == nil {
		p.client = client
	}
	close(p.discovering)
	p.discovering = nil
	p.mu.Unlock()
	return client, err
}

func (p *lazyOIDCProvider) AuthorizationURL(state, nonce, verifier, locale string) (string, error) {
	client, err := p.get(p.lifetime)
	if err != nil {
		return "", err
	}
	return client.AuthorizationURL(state, nonce, verifier, locale)
}

func (p *lazyOIDCProvider) ValidateAuthorizationResponseIssuer(ctx context.Context, issuer string) error {
	client, err := p.get(ctx)
	if err != nil {
		return err
	}
	return client.ValidateAuthorizationResponseIssuer(ctx, issuer)
}

func (p *lazyOIDCProvider) Exchange(ctx context.Context, code, verifier, nonce string) (identity.OIDCTokens, error) {
	client, err := p.get(ctx)
	if err != nil {
		return identity.OIDCTokens{}, err
	}
	return client.Exchange(ctx, code, verifier, nonce)
}

func (p *lazyOIDCProvider) Refresh(ctx context.Context, previous identity.OIDCTokens) (identity.OIDCTokens, error) {
	client, err := p.get(ctx)
	if err != nil {
		return identity.OIDCTokens{}, err
	}
	return client.Refresh(ctx, previous)
}

func (p *lazyOIDCProvider) VerifyNative(ctx context.Context, token string) (identity.OIDCIdentity, error) {
	client, err := p.get(ctx)
	if err != nil {
		return identity.OIDCIdentity{}, err
	}
	return client.VerifyNative(ctx, token)
}

func (p *lazyOIDCProvider) Revoke(ctx context.Context, refreshToken string) error {
	client, err := p.get(ctx)
	if err != nil {
		return err
	}
	return client.Revoke(ctx, refreshToken)
}

func (p *lazyOIDCProvider) EndSessionURL(locale string) (string, error) {
	client, err := p.get(p.lifetime)
	if err != nil {
		return "", err
	}
	return client.EndSessionURL(locale)
}
