//go:build linux

package cliauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreUsesPrivateAtomicRefreshOnlyStorage(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "account")
	path := filepath.Join(directory, "credentials.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor performed filesystem operations")
	}
	want := storeFixtureCredentials()
	if err = store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("credential directory is not private: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file is not private: %v", err)
	}
	want.RefreshToken = "rotated-refresh"
	if err = store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil || got != want {
		t.Fatalf("file rotation failed: %v", err)
	}
	if err = store.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(context.Background()); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("logout did not delete file: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "credentials.json.lock" {
		t.Fatal("credential content or temporary files survived deletion")
	}
}

func TestFileStoreRejectsSymlinksAndInsecurePermissions(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"directory", "ancestor-symlink", "file-symlink", "lock-symlink", "file-mode", "hardlink"} {
		t.Run(test, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "account")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "credentials.json")
			payload, _ := encodeCredentials(storeFixtureCredentials())
			switch test {
			case "directory":
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "ancestor-symlink":
				linked := filepath.Join(root, "linked")
				if err := os.Symlink(directory, linked); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(linked, "credentials.json")
			case "file-symlink", "lock-symlink":
				target := filepath.Join(root, "target")
				if err := os.WriteFile(target, payload, 0600); err != nil {
					t.Fatal(err)
				}
				link := path
				if test == "lock-symlink" {
					link += ".lock"
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			case "file-mode":
				if err := os.WriteFile(path, payload, 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				target := filepath.Join(root, "target")
				if err := os.WriteFile(target, payload, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			}
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.Load(context.Background()); !errors.Is(err, ErrCredentialsUnavailable) {
				t.Fatalf("unsafe load accepted: %v", err)
			}
			if err = store.Save(context.Background(), storeFixtureCredentials()); !errors.Is(err, ErrCredentialsUnavailable) {
				t.Fatalf("unsafe save accepted: %v", err)
			}
			if err = store.Delete(context.Background()); !errors.Is(err, ErrCredentialsUnavailable) {
				t.Fatalf("unsafe delete accepted: %v", err)
			}
		})
	}
}
