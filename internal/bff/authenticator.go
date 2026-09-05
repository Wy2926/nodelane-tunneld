package bff

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/controlapi"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

const maxBearerTokenBytes = 16 << 10

type SessionReader interface {
	ReadSession(context.Context, string) (session.Record, error)
}

type NativeVerifier interface {
	VerifyNative(context.Context, string) (identity.OIDCIdentity, error)
}

// APIAuthenticator keeps browser sessions and native access tokens as
// separate authentication media. Supplying both is rejected instead of
// choosing one implicitly.
type APIAuthenticator struct {
	sessions SessionReader
	accounts AccountStore
	verifier NativeVerifier
}

func NewAPIAuthenticator(sessions SessionReader, accounts AccountStore, verifier NativeVerifier) (*APIAuthenticator, error) {
	if sessions == nil || accounts == nil || verifier == nil {
		return nil, ErrInvalidConfiguration
	}
	return &APIAuthenticator{sessions: sessions, accounts: accounts, verifier: verifier}, nil
}

func (a *APIAuthenticator) Authenticate(ctx context.Context, request *http.Request) (controlapi.Principal, error) {
	sessionID, hasSession, sessionAmbiguous := requestSessionID(request)
	bearer, hasBearer, bearerAmbiguous := requestBearer(request)
	if sessionAmbiguous || bearerAmbiguous || hasSession == hasBearer {
		return controlapi.Principal{}, controlapi.ErrUnauthorized
	}
	if hasSession {
		record, err := a.sessions.ReadSession(ctx, sessionID)
		if err != nil {
			if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
				return controlapi.Principal{}, controlapi.ErrUnauthorized
			}
			return controlapi.Principal{}, controlapi.ErrUnavailable
		}
		if record.AccountID == "" || record.CSRFToken == "" {
			return controlapi.Principal{}, controlapi.ErrUnavailable
		}
		return controlapi.Principal{AccountID: record.AccountID, Kind: controlapi.PrincipalKindWeb, CSRFToken: record.CSRFToken}, nil
	}

	verified, err := a.verifier.VerifyNative(ctx, bearer)
	if err != nil {
		if errors.Is(err, identity.ErrOIDCUnauthorized) {
			return controlapi.Principal{}, controlapi.ErrUnauthorized
		}
		return controlapi.Principal{}, controlapi.ErrUnavailable
	}
	if verified.Issuer == "" || verified.Subject == "" || verified.ClientID == "" {
		return controlapi.Principal{}, controlapi.ErrUnauthorized
	}
	account, err := a.accounts.ResolveAccount(ctx, verified.Issuer, verified.Subject)
	if err != nil || account.ID == "" || account.IdentityIssuer != verified.Issuer || account.IdentitySubject != verified.Subject {
		return controlapi.Principal{}, controlapi.ErrUnavailable
	}
	scopes := append([]string(nil), verified.Scopes...)
	return controlapi.Principal{AccountID: account.ID, Kind: controlapi.PrincipalKindNative, Scopes: scopes}, nil
}

func requestSessionID(request *http.Request) (string, bool, bool) {
	var value string
	count := 0
	for _, cookie := range request.Cookies() {
		if cookie.Name == SessionCookieName {
			count++
			value = cookie.Value
		}
	}
	if count == 0 {
		return "", false, false
	}
	if count != 1 || !validRandomToken(value) {
		return "", false, true
	}
	return value, true, false
}

func requestBearer(request *http.Request) (string, bool, bool) {
	values := request.Header.Values("Authorization")
	if len(values) == 0 {
		return "", false, false
	}
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", false, true
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" || len(token) > maxBearerTokenBytes || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n,") {
		return "", false, true
	}
	return token, true, false
}

var _ controlapi.Authenticator = (*APIAuthenticator)(nil)
