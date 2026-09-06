//go:build linux || darwin

package cliauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func systemLocker(options SystemStoreOptions) func(context.Context) (func(), error) {
	return func(ctx context.Context) (func(), error) {
		directory := options.LockDirectory
		if directory == "" {
			configuration, err := os.UserConfigDir()
			if err != nil || !filepath.IsAbs(configuration) {
				return nil, ErrCredentialsUnavailable
			}
			directory = filepath.Join(configuration, "nodelane", "nt", "account-locks")
		}
		sum := sha256.Sum256([]byte(options.Service + "\x00" + options.Account))
		return acquireFileLock(ctx, directory, hex.EncodeToString(sum[:])+".lock")
	}
}

func acquireFileLock(ctx context.Context, directory, name string) (func(), error) {
	parent, err := openPrivateDirectory(directory)
	if err != nil {
		return nil, ErrCredentialsUnavailable
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrCredentialsUnavailable
	}
	file := os.NewFile(uintptr(fd), name)
	if !privateRegularFile(fd) {
		file.Close()
		return nil, ErrCredentialsUnavailable
	}
	for {
		if err = ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, ErrCredentialsUnavailable
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Walk with directory descriptors and O_NOFOLLOW, so no path component can
// redirect credential operations through a symlink or an ancestor rename.
func openPrivateDirectory(path string) (*os.File, error) {
	if !validAbsolutePath(path) {
		return nil, ErrCredentialsUnavailable
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrCredentialsUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return nil, ErrCredentialsUnavailable
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(fd, part, 0700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				unix.Close(fd)
				return nil, ErrCredentialsUnavailable
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return nil, ErrCredentialsUnavailable
		}
		fd = next
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&0777 != 0700 || stat.Uid != uint32(os.Geteuid()) {
		unix.Close(fd)
		return nil, ErrCredentialsUnavailable
	}
	return os.NewFile(uintptr(fd), path), nil
}

func privateRegularFile(fd int) bool {
	var stat unix.Stat_t
	return unix.Fstat(fd, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0777 == 0600 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
