package runclient

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRequestKeysHaveIndependent256BitEntropy(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		key, err := NewRequestKey()
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key)
		if err != nil || len(decoded) != 32 || seen[key] {
			t.Fatal("request key lacks independent 256-bit value")
		}
		seen[key] = true
	}
}

func TestInstallationIDIsDurableAndDoesNotOverwriteExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native", "installation-id")
	first, err := LoadOrCreateInstallationID(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateInstallationID(path)
	if err != nil || first != second || !strings.HasPrefix(first, "install_") {
		t.Fatalf("unstable installation: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != first+"\n" {
		t.Fatalf("stored installation: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("installation mode=%o", info.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("unrecognized-existing-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateInstallationID(path); err == nil || strings.Contains(err.Error(), path) {
		t.Fatalf("invalid existing file accepted: %v", err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "unrecognized-existing-data" {
		t.Fatal("overwrote existing state")
	}
}

func TestInstallationIDConcurrentProcessesPublishOneCompleteValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installation-id")
	const workers = 8
	var wg sync.WaitGroup
	outputs := make([][]byte, workers)
	errors := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestInstallationIDProcessHelper$")
			command.Env = append(os.Environ(), "NT_TEST_INSTALLATION_PATH="+path)
			outputs[index], errors[index] = command.CombinedOutput()
		}(i)
	}
	wg.Wait()
	var first string
	for i := range outputs {
		if errors[i] != nil {
			t.Fatalf("process %d: %s %v", i, outputs[i], errors[i])
		}
		value := strings.TrimSpace(string(outputs[i]))
		if first == "" {
			first = value
		}
		if value != first || !strings.HasPrefix(value, "install_") {
			t.Fatalf("concurrent process observed partial or different value: %q", value)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "installation-id" {
		t.Fatalf("temporary files were not removed: %v %v", entries, err)
	}
}

func TestInstallationIDProcessHelper(t *testing.T) {
	path := os.Getenv("NT_TEST_INSTALLATION_PATH")
	if path == "" {
		return
	}
	id, err := LoadOrCreateInstallationID(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(id)
	os.Exit(0)
}
