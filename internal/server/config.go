package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr string
	DevMode    bool
	ReleaseDir string
	LogLevel   string

	PublicScheme string
	PublicDomain string
	NodeID       string

	FRPServerAddr    string
	FRPServerPort    int
	FRPAuthToken     string
	FRPTLSServerName string
	FRPBandwidth     string

	DatabaseURL   string
	RedisAddr     string
	RedisPassword string
	RedisPrefix   string

	TokenPepper     string
	TunnelJWTSecret []byte
	AdminToken      string

	TunnelTTL          time.Duration
	MaxPerClient       int
	MaxPerIP           int
	TCPPortStart       int
	TCPPortEnd         int
	UDPPortStart       int
	UDPPortEnd         int
	TrustedProxyRanges []netip.Prefix
}

func LoadConfig() (Config, error) {
	cfg := Config{
		ListenAddr:       env("LISTEN_ADDR", ":9000"),
		DevMode:          envBool("DEV_MODE", false),
		ReleaseDir:       os.Getenv("RELEASE_DIR"),
		LogLevel:         strings.ToLower(env("LOG_LEVEL", "info")),
		PublicScheme:     strings.ToLower(strings.TrimSpace(env("PUBLIC_SCHEME", "http"))),
		PublicDomain:     env("PUBLIC_DOMAIN", "tunnel.nodelane.net"),
		NodeID:           env("NODE_ID", "primary"),
		FRPServerAddr:    env("FRP_SERVER_ADDR", "tunnel.nodelane.net"),
		FRPServerPort:    envInt("FRP_SERVER_PORT", 7000),
		FRPAuthToken:     os.Getenv("FRP_AUTH_TOKEN"),
		FRPTLSServerName: env("FRP_TLS_SERVER_NAME", "tunnel.nodelane.net"),
		FRPBandwidth:     env("FRP_BANDWIDTH_LIMIT", "5MB"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		RedisAddr:        os.Getenv("REDIS_ADDR"),
		RedisPassword:    os.Getenv("REDIS_PASSWORD"),
		RedisPrefix:      env("REDIS_PREFIX", "nodelane:tunnel"),
		TokenPepper:      os.Getenv("TOKEN_PEPPER"),
		AdminToken:       os.Getenv("ADMIN_TOKEN"),
		TunnelTTL:        envDuration("TUNNEL_TTL", time.Hour),
		MaxPerClient:     envInt("MAX_TUNNELS_PER_CLIENT", 1),
		MaxPerIP:         envInt("MAX_TUNNELS_PER_IP", 2),
		TCPPortStart:     envInt("TCP_PORT_START", 20000),
		TCPPortEnd:       envInt("TCP_PORT_END", 29999),
		UDPPortStart:     envInt("UDP_PORT_START", 30000),
		UDPPortEnd:       envInt("UDP_PORT_END", 39999),
	}

	secret := os.Getenv("TUNNEL_JWT_SECRET")
	if decoded, err := base64.RawURLEncoding.DecodeString(secret); err == nil && len(decoded) >= 32 {
		cfg.TunnelJWTSecret = decoded
	} else {
		cfg.TunnelJWTSecret = []byte(secret)
	}

	for _, raw := range strings.Split(os.Getenv("TRUSTED_PROXY_CIDRS"), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse TRUSTED_PROXY_CIDRS entry %q: %w", raw, err)
		}
		cfg.TrustedProxyRanges = append(cfg.TrustedProxyRanges, prefix.Masked())
	}

	if cfg.DevMode {
		if cfg.TokenPepper == "" {
			cfg.TokenPepper = "development-token-pepper-change-me"
		}
		if len(cfg.TunnelJWTSecret) < 32 {
			cfg.TunnelJWTSecret = []byte("development-jwt-secret-change-me-32-bytes")
		}
		if cfg.FRPAuthToken == "" {
			cfg.FRPAuthToken = "development-frp-token"
		}
		if cfg.AdminToken == "" {
			cfg.AdminToken = "development-admin-token"
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var missing []string
	if c.PublicDomain == "" {
		missing = append(missing, "PUBLIC_DOMAIN")
	}
	if c.FRPServerAddr == "" {
		missing = append(missing, "FRP_SERVER_ADDR")
	}
	if c.FRPAuthToken == "" {
		missing = append(missing, "FRP_AUTH_TOKEN")
	}
	if c.TokenPepper == "" {
		missing = append(missing, "TOKEN_PEPPER")
	}
	if len(c.TunnelJWTSecret) < 32 {
		missing = append(missing, "TUNNEL_JWT_SECRET(>=32 bytes)")
	}
	if !c.DevMode && c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if !c.DevMode && c.RedisAddr == "" {
		missing = append(missing, "REDIS_ADDR")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if c.PublicScheme != "http" && c.PublicScheme != "https" {
		return errors.New("PUBLIC_SCHEME must be http or https")
	}
	if c.FRPServerPort < 1 || c.FRPServerPort > 65535 {
		return errors.New("FRP_SERVER_PORT must be between 1 and 65535")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return errors.New("LOG_LEVEL must be debug, info, warn, or error")
	}
	if c.TCPPortStart < 1024 || c.TCPPortStart > c.TCPPortEnd || c.TCPPortEnd > 65535 {
		return errors.New("invalid TCP port range")
	}
	if c.UDPPortStart < 1024 || c.UDPPortStart > c.UDPPortEnd || c.UDPPortEnd > 65535 {
		return errors.New("invalid UDP port range")
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
