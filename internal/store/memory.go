package store

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

type Memory struct {
	mu      sync.RWMutex
	clients map[string]domain.Client
	tokens  map[string]domain.ClientToken
	tunnels map[string]domain.Tunnel
	bans    []domain.NetworkBan
}

func NewMemory() *Memory {
	return &Memory{
		clients: make(map[string]domain.Client),
		tokens:  make(map[string]domain.ClientToken),
		tunnels: make(map[string]domain.Tunnel),
	}
}

func (m *Memory) CreateClient(_ context.Context, client domain.Client, token domain.ClientToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.clients[client.ID]; exists {
		return domain.ErrConflict
	}
	if _, exists := m.tokens[token.ID]; exists || token.ClientID != client.ID {
		return domain.ErrConflict
	}
	m.clients[client.ID] = client
	m.tokens[token.ID] = token
	return nil
}

func (m *Memory) GetClient(_ context.Context, id string) (domain.Client, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, ok := m.clients[id]
	if !ok {
		return domain.Client{}, domain.ErrNotFound
	}
	return client, nil
}

func (m *Memory) GetClientToken(_ context.Context, id string) (domain.ClientToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	token, ok := m.tokens[id]
	if !ok {
		return domain.ClientToken{}, domain.ErrNotFound
	}
	return token, nil
}

func (m *Memory) TouchClientToken(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	token, ok := m.tokens[id]
	if !ok {
		return domain.ErrNotFound
	}
	token.LastUsedAt = &at
	m.tokens[id] = token
	return nil
}

func (m *Memory) TouchClient(_ context.Context, id string, ip netip.Addr, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, ok := m.clients[id]
	if !ok {
		return domain.ErrNotFound
	}
	client.LastIP = ip
	client.LastSeenAt = at
	m.clients[id] = client
	return nil
}

func (m *Memory) BanClient(_ context.Context, id, reason string, until *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, ok := m.clients[id]
	if !ok {
		return domain.ErrNotFound
	}
	client.Status = domain.ClientBanned
	client.BanReason = reason
	client.BannedUntil = until
	m.clients[id] = client
	return nil
}

func (m *Memory) CreateTunnel(_ context.Context, tunnel domain.Tunnel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.tunnels[tunnel.ID]; exists {
		return domain.ErrConflict
	}
	for id, existing := range m.tunnels {
		if existing.Status == domain.TunnelClosed || existing.Status == domain.TunnelExpired {
			continue
		}
		if !existing.ExpiresAt.After(tunnel.CreatedAt) {
			existing.Status = domain.TunnelExpired
			closedAt := tunnel.CreatedAt
			existing.ClosedAt = &closedAt
			m.tunnels[id] = existing
			continue
		}
		if tunnel.Subdomain != "" && existing.Subdomain == tunnel.Subdomain {
			return domain.ErrConflict
		}
		if tunnel.RemotePort != 0 && existing.Protocol == tunnel.Protocol && existing.RemotePort == tunnel.RemotePort {
			return domain.ErrConflict
		}
	}
	m.tunnels[tunnel.ID] = tunnel
	return nil
}

func (m *Memory) GetTunnel(_ context.Context, id string) (domain.Tunnel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tunnel, ok := m.tunnels[id]
	if !ok {
		return domain.Tunnel{}, domain.ErrNotFound
	}
	return tunnel, nil
}

func (m *Memory) UpdateTunnelConnected(_ context.Context, id string, ip netip.Addr, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tunnel, ok := m.tunnels[id]
	if !ok {
		return domain.ErrNotFound
	}
	tunnel.Status = domain.TunnelOnline
	tunnel.ConnectedIP = ip
	tunnel.ConnectedAt = &at
	m.tunnels[id] = tunnel
	return nil
}

func (m *Memory) CloseTunnel(_ context.Context, id string, status domain.TunnelStatus, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tunnel, ok := m.tunnels[id]
	if !ok {
		return domain.ErrNotFound
	}
	tunnel.Status = status
	tunnel.ClosedAt = &at
	m.tunnels[id] = tunnel
	return nil
}

func (m *Memory) IsIPBanned(_ context.Context, ip netip.Addr, scope string, now time.Time) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ban := range m.bans {
		if ban.ExpiresAt != nil && !ban.ExpiresAt.After(now) {
			continue
		}
		if ban.Scope != "both" && ban.Scope != scope {
			continue
		}
		if ban.Network.Contains(ip) {
			return true, nil
		}
	}
	return false, nil
}

func (m *Memory) CreateNetworkBan(_ context.Context, ban domain.NetworkBan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bans = append(m.bans, ban)
	return nil
}

func (m *Memory) Close() error { return nil }
