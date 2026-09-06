package cliauth

import (
	"context"
	"errors"
	"testing"
)

func TestLogoutStoredClearsUnreadableOrInvalidCredentials(t *testing.T) {
	for _, test := range []struct {
		name        string
		credentials Credentials
		loadErr     error
	}{
		{name: "decode failure", loadErr: ErrCredentialsUnavailable},
		{name: "invalid identity", credentials: Credentials{Issuer: "invalid issuer", ClientID: "native", Resource: "invalid resource", RefreshToken: "fixture-secret"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{present: true, credentials: test.credentials, loadErr: test.loadErr}
			err := LogoutStored(context.Background(), store)
			if !errors.Is(err, ErrRevocationUnconfirmed) || errors.Is(err, ErrCredentialsUnavailable) || store.present || store.deletes != 1 {
				t.Fatalf("cleanup failed: present=%v deletes=%d err=%v", store.present, store.deletes, err)
			}
		})
	}
}

func TestLogoutStoredReportsLocalDeletionFailure(t *testing.T) {
	store := &memoryStore{present: true, loadErr: ErrCredentialsUnavailable, deleteErr: errors.New("fixture deletion refused")}
	err := LogoutStored(context.Background(), store)
	if !errors.Is(err, ErrCredentialsUnavailable) || !errors.Is(err, ErrRevocationUnconfirmed) || !store.present || store.deletes != 1 {
		t.Fatalf("failed deletion was misreported: present=%v deletes=%d err=%v", store.present, store.deletes, err)
	}
}

func TestLogoutStoredStillClearsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &memoryStore{present: true, loadErr: ErrCredentialsUnavailable}
	if err := LogoutStored(ctx, store); !errors.Is(err, ErrRevocationUnconfirmed) || store.present {
		t.Fatalf("cancellation skipped cleanup: present=%v err=%v", store.present, err)
	}
}

func TestLogoutStoredAbsentIsIdempotent(t *testing.T) {
	store := &memoryStore{}
	if err := LogoutStored(context.Background(), store); err != nil {
		t.Fatal(err)
	}
}
