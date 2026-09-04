package server

import "testing"

func TestLoadConfigDefaultsPublicSchemeToHTTP(t *testing.T) {
	t.Setenv("DEV_MODE", "true")
	t.Setenv("PUBLIC_SCHEME", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicScheme != "http" {
		t.Fatalf("PublicScheme = %q, want http", cfg.PublicScheme)
	}
}
