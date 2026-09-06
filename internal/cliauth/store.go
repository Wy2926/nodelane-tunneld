package cliauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

var ErrFileStoreUnsupported = errors.New("plaintext file credentials are unsupported on this platform; use the system credential store")

// TransactionalStore serializes an entire read/refresh/write or logout operation
// across processes. The callback's Store is valid only for that transaction.
type TransactionalStore interface {
	Store
	Transaction(context.Context, func(Store) error) error
}

type KeyringBackend interface {
	Get(service, account string) (string, error)
	Set(service, account, password string) error
	Delete(service, account string) error
}

type SystemStoreOptions struct {
	Service string
	Account string
	Backend KeyringBackend
	// LockDirectory overrides the private Unix lock directory, not credential storage.
	LockDirectory string
}

// NewSystemStore is lazy: construction never reads or writes system credentials.
func NewSystemStore(options SystemStoreOptions) (Store, error) {
	if options.Service == "" {
		options.Service = "net.nodelane.nt"
	}
	if options.Account == "" {
		options.Account = "default"
	}
	if !safeValue(options.Service, 256) || !safeValue(options.Account, 256) || strings.Contains(options.Service, ":") || strings.Contains(options.Account, ":") {
		return nil, ErrInvalidConfiguration
	}
	// Credential Manager joins these fields with ':' and compares target names
	// without case; use the same unambiguous identity for cross-platform locks.
	options.Service, options.Account = strings.ToLower(options.Service), strings.ToLower(options.Account)
	if options.LockDirectory != "" && !validAbsolutePath(options.LockDirectory) {
		return nil, ErrInvalidConfiguration
	}
	if options.Backend == nil {
		options.Backend = systemKeyring{}
	}
	return &lockedStore{raw: &keyringStore{options: options}, acquire: systemLocker(options)}, nil
}

// NewFileStore is an explicit lower-security fallback. It never silently replaces
// an unavailable system keyring, and is intentionally unsupported on Windows.
func NewFileStore(absolutePath string) (Store, error) {
	if !validAbsolutePath(absolutePath) || filepath.Dir(absolutePath) == absolutePath {
		return nil, ErrInvalidConfiguration
	}
	return platformFileStore(absolutePath)
}

func validAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) <= 4096
}

type systemKeyring struct{}

func (systemKeyring) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}
func (systemKeyring) Set(service, account, password string) error {
	return keyring.Set(service, account, password)
}
func (systemKeyring) Delete(service, account string) error { return keyring.Delete(service, account) }

type keyringStore struct{ options SystemStoreOptions }

func (s *keyringStore) Load(ctx context.Context) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	encoded, err := s.options.Backend.Get(s.options.Service, s.options.Account)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credentials{}, ErrNoCredentials
	}
	if err != nil {
		return Credentials{}, ErrCredentialsUnavailable
	}
	return decodeCredentials([]byte(encoded))
}

func (s *keyringStore) Save(ctx context.Context, credentials Credentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := encodeCredentials(credentials)
	if err != nil {
		return err
	}
	if err = s.options.Backend.Set(s.options.Service, s.options.Account, string(encoded)); err != nil {
		return ErrCredentialsUnavailable
	}
	// The pinned Secret Service backend can return nil for a dismissed prompt.
	// Confirm the exact record while the caller still holds the transaction lock.
	stored, err := s.options.Backend.Get(s.options.Service, s.options.Account)
	if err != nil || stored != string(encoded) {
		return ErrCredentialsUnavailable
	}
	return nil
}

func (s *keyringStore) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.options.Backend.Delete(s.options.Service, s.options.Account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return ErrCredentialsUnavailable
	}
	if _, err := s.options.Backend.Get(s.options.Service, s.options.Account); !errors.Is(err, keyring.ErrNotFound) {
		return ErrCredentialsUnavailable
	}
	return nil
}

type lockedStore struct {
	raw     Store
	acquire func(context.Context) (func(), error)
}

func (s *lockedStore) Transaction(ctx context.Context, action func(Store) error) error {
	if action == nil {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return err
	}
	return action(s.raw)
}

func (s *lockedStore) Load(ctx context.Context) (credentials Credentials, err error) {
	err = s.Transaction(ctx, func(store Store) error { credentials, err = store.Load(ctx); return err })
	return credentials, err
}
func (s *lockedStore) Save(ctx context.Context, credentials Credentials) error {
	return s.Transaction(ctx, func(store Store) error { return store.Save(ctx, credentials) })
}
func (s *lockedStore) Delete(ctx context.Context) error {
	return s.Transaction(ctx, func(store Store) error { return store.Delete(ctx) })
}

func encodeCredentials(credentials Credentials) ([]byte, error) {
	if !validCredentials(credentials) {
		return nil, ErrCredentialsUnavailable
	}
	encoded, err := json.Marshal(credentials)
	if err != nil || len(encoded) > maxProviderBody {
		return nil, ErrCredentialsUnavailable
	}
	return encoded, nil
}

func decodeCredentials(encoded []byte) (Credentials, error) {
	if len(encoded) == 0 || len(encoded) > maxProviderBody {
		return Credentials{}, ErrCredentialsUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var credentials Credentials
	if err := decoder.Decode(&credentials); err != nil || !validCredentials(credentials) {
		return Credentials{}, ErrCredentialsUnavailable
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Credentials{}, ErrCredentialsUnavailable
	}
	return credentials, nil
}
