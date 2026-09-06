package cliauth

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/oauth2"
)

func TestProviderRecoveryErrorsHaveStableIdentities(t *testing.T) {
	for code, want := range map[string]error{
		"access_denied": ErrAuthorizationDenied,
		"expired_token": ErrAuthorizationExpired,
		"invalid_grant": ErrAuthorizationRevoked,
	} {
		got := sanitizeProviderError(context.Background(), &oauth2.RetrieveError{ErrorCode: code})
		if !errors.Is(got, want) {
			t.Fatalf("provider code=%s produced unclassifiable error=%v", code, got)
		}
	}
}
