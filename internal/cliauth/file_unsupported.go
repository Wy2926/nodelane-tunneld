//go:build !linux

package cliauth

func platformFileStore(string) (Store, error) { return nil, ErrFileStoreUnsupported }
