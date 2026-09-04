package domain

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"time"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrBanned       = errors.New("banned")
	ErrLimitReached = errors.New("limit reached")
	ErrConflict     = errors.New("conflict")
)

type ClientStatus string

const (
	ClientActive  ClientStatus = "active"
	ClientLimited ClientStatus = "limited"
	ClientBanned  ClientStatus = "banned"
)

type Client struct {
	ID             string       `json:"id"`
	AccountID      *string      `json:"account_id,omitempty"`
	Status         ClientStatus `json:"status"`
	BanReason      string       `json:"ban_reason,omitempty"`
	BannedUntil    *time.Time   `json:"banned_until,omitempty"`
	RegistrationIP netip.Addr   `json:"registration_ip"`
	LastIP         netip.Addr   `json:"last_ip"`
	CreatedAt      time.Time    `json:"created_at"`
	LastSeenAt     time.Time    `json:"last_seen_at"`
}

type ClientToken struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"client_id"`
	TokenHash  string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func (t ClientToken) IsValid(now time.Time) bool {
	return t.RevokedAt == nil && (t.ExpiresAt == nil || t.ExpiresAt.After(now))
}

func (c Client) IsBanned(now time.Time) bool {
	if c.Status != ClientBanned {
		return false
	}
	return c.BannedUntil == nil || c.BannedUntil.After(now)
}

type TunnelStatus string

const (
	TunnelReserved TunnelStatus = "reserved"
	TunnelOnline   TunnelStatus = "online"
	TunnelClosed   TunnelStatus = "closed"
	TunnelExpired  TunnelStatus = "expired"
)

type Tunnel struct {
	ID          string       `json:"id"`
	ClientID    string       `json:"client_id"`
	TokenID     string       `json:"token_id"`
	Protocol    string       `json:"protocol"`
	NodeID      string       `json:"node_id"`
	ProxyName   string       `json:"proxy_name"`
	Subdomain   string       `json:"subdomain,omitempty"`
	RemotePort  int          `json:"remote_port,omitempty"`
	RequestIP   netip.Addr   `json:"request_ip"`
	ConnectedIP netip.Addr   `json:"connected_ip,omitempty"`
	Status      TunnelStatus `json:"status"`
	CreatedAt   time.Time    `json:"created_at"`
	ConnectedAt *time.Time   `json:"connected_at,omitempty"`
	ExpiresAt   time.Time    `json:"expires_at"`
	ClosedAt    *time.Time   `json:"closed_at,omitempty"`
}

func (t Tunnel) ResourceKey() string {
	switch t.Protocol {
	case "http":
		return "subdomain:" + t.Subdomain
	case "tcp", "udp":
		return t.Protocol + "-port:" + strconv.Itoa(t.RemotePort)
	default:
		return "unknown:" + t.ID
	}
}

type NetworkBan struct {
	ID        string       `json:"id"`
	Network   netip.Prefix `json:"network"`
	Scope     string       `json:"scope"`
	Reason    string       `json:"reason"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
}

type Repository interface {
	CreateClient(context.Context, Client, ClientToken) error
	GetClient(context.Context, string) (Client, error)
	GetClientToken(context.Context, string) (ClientToken, error)
	TouchClientToken(context.Context, string, time.Time) error
	TouchClient(context.Context, string, netip.Addr, time.Time) error
	BanClient(context.Context, string, string, *time.Time) error

	CreateTunnel(context.Context, Tunnel) error
	GetTunnel(context.Context, string) (Tunnel, error)
	UpdateTunnelConnected(context.Context, string, netip.Addr, time.Time) error
	CloseTunnel(context.Context, string, TunnelStatus, time.Time) error

	IsIPBanned(context.Context, netip.Addr, string, time.Time) (bool, error)
	CreateNetworkBan(context.Context, NetworkBan) error
	Close() error
}

type LeaseManager interface {
	Reserve(ctx context.Context, clientID, ipKey, tunnelID, resourceKey string, expiresAt time.Time, maxPerClient, maxPerIP int) error
	Release(ctx context.Context, clientID, ipKey, tunnelID, resourceKey string) error
	Close() error
}
