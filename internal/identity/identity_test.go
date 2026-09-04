package identity

import (
	"strings"
	"testing"
	"time"
)

func TestClientToken(t *testing.T) {
	_, tokenID, token, err := NewClientCredential()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseClientToken(token)
	if err != nil || parsed != tokenID {
		t.Fatalf("parse got %q, %v", parsed, err)
	}
	hash := HashToken("pepper", token)
	if !TokenHashEqual(hash, HashToken("pepper", token)) || TokenHashEqual(hash, HashToken("other", token)) {
		t.Fatal("token hash comparison failed")
	}
}

func TestTunnelJWT(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	want := TunnelClaims{
		Issuer: "nodelane-tunnel", Audience: "frp-plugin", Subject: "cli_a", SessionID: "tun_a",
		TokenID: "tok_a", Node: "cn1", Protocol: "http", ProxyName: "tun_a", Subdomain: "demo",
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	}
	token, err := SignTunnelToken([]byte("secret"), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyTunnelToken([]byte("secret"), token, "nodelane-tunnel", "frp-plugin", now)
	if err != nil || got.SessionID != want.SessionID {
		t.Fatalf("verify got %#v, %v", got, err)
	}
	if _, err := VerifyTunnelToken([]byte("wrong"), token, "nodelane-tunnel", "frp-plugin", now); err == nil {
		t.Fatal("wrong key was accepted")
	}
}

func TestSlug(t *testing.T) {
	slug, err := NewSlug()
	if err != nil {
		t.Fatal(err)
	}
	if len(slug) < 12 || strings.Count(slug, "-") != 2 {
		t.Fatalf("unexpected slug %q", slug)
	}
}
