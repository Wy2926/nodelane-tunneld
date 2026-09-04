package store

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

func TestCreateTunnelReclaimsExpiredResource(t *testing.T) {
	repository := NewMemory()
	now := time.Now().UTC()
	old := domain.Tunnel{
		ID: "tun_old", ClientID: "cli_old", TokenID: "tok_old", Protocol: "tcp",
		NodeID: "primary", ProxyName: "tun_old", RemotePort: 20001,
		RequestIP: netip.MustParseAddr("192.0.2.1"), Status: domain.TunnelOnline,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	if err := repository.CreateTunnel(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	fresh := domain.Tunnel{
		ID: "tun_fresh", ClientID: "cli_fresh", TokenID: "tok_fresh", Protocol: "tcp",
		NodeID: "primary", ProxyName: "tun_fresh", RemotePort: old.RemotePort,
		RequestIP: netip.MustParseAddr("192.0.2.2"), Status: domain.TunnelReserved,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := repository.CreateTunnel(context.Background(), fresh); err != nil {
		t.Fatalf("reuse expired port: %v", err)
	}

	expired, err := repository.GetTunnel(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != domain.TunnelExpired || expired.ClosedAt == nil {
		t.Fatalf("old tunnel was not expired: %#v", expired)
	}
}
