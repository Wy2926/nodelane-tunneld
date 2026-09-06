//go:build linux

package cliauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type fileStore struct{ directory, name string }

func platformFileStore(path string) (Store, error) {
	raw := &fileStore{directory: filepath.Dir(path), name: filepath.Base(path)}
	return &lockedStore{raw: raw, acquire: func(ctx context.Context) (func(), error) {
		return acquireFileLock(ctx, raw.directory, raw.name+".lock")
	}}, nil
}

func (s *fileStore) Load(ctx context.Context) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	parent, err := openPrivateDirectory(s.directory)
	if err != nil {
		return Credentials{}, ErrCredentialsUnavailable
	}
	defer parent.Close()
	file, err := s.openExisting(parent)
	if err != nil {
		return Credentials{}, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxProviderBody+1))
	if err != nil {
		return Credentials{}, ErrCredentialsUnavailable
	}
	return decodeCredentials(encoded)
}

func (s *fileStore) Save(ctx context.Context, credentials Credentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := encodeCredentials(credentials)
	if err != nil {
		return err
	}
	parent, err := openPrivateDirectory(s.directory)
	if err != nil {
		return ErrCredentialsUnavailable
	}
	defer parent.Close()
	if existing, err := s.openExisting(parent); err == nil {
		existing.Close()
	} else if !errors.Is(err, ErrNoCredentials) {
		return ErrCredentialsUnavailable
	}
	var suffix [16]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		return ErrCredentialsUnavailable
	}
	name := "." + s.name + "-" + hex.EncodeToString(suffix[:]) + ".tmp"
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrCredentialsUnavailable
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	defer unix.Unlinkat(int(parent.Fd()), name, 0)
	if err = file.Chmod(0600); err != nil {
		return ErrCredentialsUnavailable
	}
	if _, err = file.Write(encoded); err != nil {
		return ErrCredentialsUnavailable
	}
	if err = file.Sync(); err != nil {
		return ErrCredentialsUnavailable
	}
	if err = file.Close(); err != nil {
		return ErrCredentialsUnavailable
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = unix.Renameat(int(parent.Fd()), name, int(parent.Fd()), s.name); err != nil {
		return ErrCredentialsUnavailable
	}
	if err = unix.Fsync(int(parent.Fd())); err != nil {
		return ErrCredentialsUnavailable
	}
	return nil
}

func (s *fileStore) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := openPrivateDirectory(s.directory)
	if err != nil {
		return ErrCredentialsUnavailable
	}
	defer parent.Close()
	file, err := s.openExisting(parent)
	if errors.Is(err, ErrNoCredentials) {
		return nil
	}
	if err != nil {
		return ErrCredentialsUnavailable
	}
	file.Close()
	if err = unix.Unlinkat(int(parent.Fd()), s.name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return ErrCredentialsUnavailable
	}
	if err = unix.Fsync(int(parent.Fd())); err != nil {
		return ErrCredentialsUnavailable
	}
	return nil
}

func (s *fileStore) openExisting(parent *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), s.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, ErrNoCredentials
	}
	if err != nil {
		return nil, ErrCredentialsUnavailable
	}
	file := os.NewFile(uintptr(fd), s.name)
	if !privateRegularFile(fd) {
		file.Close()
		return nil, ErrCredentialsUnavailable
	}
	return file, nil
}
