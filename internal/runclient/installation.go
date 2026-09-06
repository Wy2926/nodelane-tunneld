package runclient

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var ErrInstallationState = errors.New("installation_state_unavailable")
var ErrRandomUnavailable = errors.New("random_source_unavailable")

func NewRequestKey() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", ErrRandomUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

// LoadOrCreateInstallationID keeps anonymous installation identity separate from
// account credentials. An O_EXCL temporary inode is synced before publication.
func LoadOrCreateInstallationID(path string) (string, error) {
	if path == "" || strings.TrimSpace(path) != path {
		return "", ErrInstallationState
	}
	if id, err := readInstallationID(path); !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", ErrInstallationState
	}
	key, err := NewRequestKey()
	if err != nil {
		return "", err
	}
	id := "install_" + key
	temporary, err := os.CreateTemp(directory, ".nt-installation-*")
	if err != nil {
		return "", ErrInstallationState
	}
	defer os.Remove(temporary.Name())
	if _, err := io.WriteString(temporary, id+"\n"); err != nil {
		_ = temporary.Close()
		return "", ErrInstallationState
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", ErrInstallationState
	}
	if err := temporary.Close(); err != nil {
		return "", ErrInstallationState
	}
	// Link is an atomic no-replace publication on supported local filesystems;
	// concurrent readers can never observe an empty or partially written ID.
	if err := os.Link(temporary.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readInstallationID(path)
		}
		return "", ErrInstallationState
	}
	if runtime.GOOS != "windows" {
		parent, err := os.Open(directory)
		if err != nil {
			return "", ErrInstallationState
		}
		err = parent.Sync()
		_ = parent.Close()
		if err != nil {
			return "", ErrInstallationState
		}
	}
	return id, nil
}

func readInstallationID(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", os.ErrNotExist
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return "", ErrInstallationState
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrInstallationState
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return "", ErrInstallationState
	}
	data, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || len(data) > 256 {
		return "", ErrInstallationState
	}
	id := strings.TrimSuffix(string(data), "\n")
	if !strings.HasPrefix(id, "install_") {
		return "", ErrInstallationState
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "install_"))
	if err != nil || len(decoded) != 32 {
		return "", ErrInstallationState
	}
	return id, nil
}
