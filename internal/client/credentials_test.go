package client

import (
	"path/filepath"
	"testing"
)

func TestCredentialsPathUsesNodeLaneTunnelDirectory(t *testing.T) {
	t.Setenv("NT_CREDENTIALS_FILE", "")
	path, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := filepath.Join("nodelane", "tunnel", "credentials.json")
	if len(path) < len(wantSuffix) || path[len(path)-len(wantSuffix):] != wantSuffix {
		t.Fatalf("credentials path = %q, want suffix %q", path, wantSuffix)
	}
}
