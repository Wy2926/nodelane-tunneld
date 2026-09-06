//go:build !windows && !linux && !darwin

package cliauth

import "context"

func systemLocker(SystemStoreOptions) func(context.Context) (func(), error) {
	return func(context.Context) (func(), error) { return nil, ErrCredentialsUnavailable }
}
