// Package controlserver composes the persistent Tunnel control plane.
package controlserver

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const APIResource = "https://tunnel.nodelane.net/api"

var secretConfigNames = []string{
	"CONTROL_LAUNCH_PEPPER", "CONTROL_RUN_PEPPER", "CONTROL_REPLAY_KEY",
	"SESSION_ENCRYPTION_KEY", "ANONYMOUS_CREDENTIAL_PEPPER", "ANONYMOUS_REPLAY_KEY", "ANONYMOUS_FENCE_OWNER_TOKEN",
}

type Config struct {
	ListenAddr, PluginListenAddr, LogLevel, ReleaseDir                                 string
	PublicOrigin, PublicDomain                                                         string
	DatabaseURL, RedisAddr, RedisPassword, RedisPrefix                                 string
	RedisDB                                                                            int
	LaunchPepper, RunPepper, ReplayKey, SessionKey                                     []byte
	AnonymousPepper, AnonymousReplayKey, AnonymousFenceToken                           []byte
	OIDCIssuer, OIDCResource, OIDCWebClientID, OIDCWebClientSecret, OIDCNativeClientID string
	FRPServerAddr, FRPTLSServerName, FRPTrustedCAFile, FRPBandwidth, FRPSConfigFile    string
	FRPServerPort, FRPSBindPort                                                        int
	FRPSAdminURL, FRPSAdminUsername, FRPSAdminPassword                                 string
	TCPPortStart, TCPPortEnd, UDPPortStart, UDPPortEnd                                 int
	TrustedProxyRanges                                                                 []netip.Prefix
}

func LoadConfig() (Config, error) { return parseConfig(os.Getenv) }

func parseConfig(lookup func(string) string) (Config, error) {
	value := func(name, fallback string) string {
		if raw := lookup(name); raw != "" {
			return raw
		}
		return fallback
	}
	if lookup("FRP_AUTH_TOKEN") != "" {
		return Config{}, errors.New("FRP_AUTH_TOKEN must be empty in per-run plugin authorization mode")
	}
	cfg := Config{
		ListenAddr: value("LISTEN_ADDR", ":9000"), PluginListenAddr: value("FRP_PLUGIN_LISTEN_ADDR", "127.0.0.1:9001"),
		LogLevel: value("LOG_LEVEL", "info"), ReleaseDir: lookup("RELEASE_DIR"),
		PublicOrigin: value("PUBLIC_ORIGIN", "https://tunnel.nodelane.net"), PublicDomain: value("PUBLIC_DOMAIN", "tunnel.nodelane.net"),
		DatabaseURL: lookup("DATABASE_URL"), RedisAddr: lookup("REDIS_ADDR"), RedisPassword: lookup("REDIS_PASSWORD"), RedisPrefix: value("REDIS_PREFIX", "nodelane:tunnel:control"),
		OIDCIssuer: value("OIDC_ISSUER", "https://auth.nodelane.net/oidc"), OIDCResource: APIResource,
		OIDCWebClientID: lookup("OIDC_WEB_CLIENT_ID"), OIDCWebClientSecret: lookup("OIDC_WEB_CLIENT_SECRET"), OIDCNativeClientID: lookup("OIDC_NATIVE_CLIENT_ID"),
		FRPServerAddr: value("FRP_SERVER_ADDR", "tunnel.nodelane.net"), FRPTLSServerName: value("FRP_TLS_SERVER_NAME", "tunnel.nodelane.net"),
		FRPTrustedCAFile: lookup("FRP_TRUSTED_CA_FILE"), FRPBandwidth: value("FRP_BANDWIDTH_LIMIT", "5MB"), FRPSConfigFile: lookup("FRPS_CONFIG_FILE"),
		FRPSAdminURL: lookup("FRPS_ADMIN_URL"), FRPSAdminUsername: lookup("FRPS_ADMIN_USERNAME"), FRPSAdminPassword: lookup("FRPS_ADMIN_PASSWORD"),
	}
	for _, item := range []struct {
		name     string
		target   *int
		fallback int
	}{
		{"FRP_SERVER_PORT", &cfg.FRPServerPort, 7000}, {"REDIS_DB", &cfg.RedisDB, 0},
		{"TCP_PORT_START", &cfg.TCPPortStart, 20000}, {"TCP_PORT_END", &cfg.TCPPortEnd, 29999},
		{"UDP_PORT_START", &cfg.UDPPortStart, 30000}, {"UDP_PORT_END", &cfg.UDPPortEnd, 39999},
	} {
		*item.target = item.fallback
		if raw := lookup(item.name); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be an integer", item.name)
			}
			*item.target = parsed
		}
	}
	cfg.FRPSBindPort = cfg.FRPServerPort
	if raw := lookup("FRPS_BIND_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, errors.New("FRPS_BIND_PORT must be an integer")
		}
		if port < 1 || port > 65535 {
			return Config{}, errors.New("invalid FRPS_BIND_PORT")
		}
		cfg.FRPSBindPort = port
	}
	secrets := []*[]byte{&cfg.LaunchPepper, &cfg.RunPepper, &cfg.ReplayKey, &cfg.SessionKey, &cfg.AnonymousPepper, &cfg.AnonymousReplayKey, &cfg.AnonymousFenceToken}
	for index, name := range secretConfigNames {
		raw := lookup(name)
		decoded, err := base64.StdEncoding.Strict().DecodeString(raw)
		if err != nil {
			decoded, err = base64.RawURLEncoding.Strict().DecodeString(raw)
		}
		if err != nil || len(decoded) != 32 {
			return Config{}, fmt.Errorf("%s must contain 32 base64-encoded bytes", name)
		}
		*secrets[index] = decoded
	}
	for _, raw := range strings.Split(lookup("TRUSTED_PROXY_CIDRS"), ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, errors.New("TRUSTED_PROXY_CIDRS contains an invalid prefix")
		}
		cfg.TrustedProxyRanges = append(cfg.TrustedProxyRanges, prefix.Masked())
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	for _, item := range []struct{ name, value string }{
		{"DATABASE_URL", c.DatabaseURL}, {"REDIS_ADDR", c.RedisAddr}, {"FRP_TRUSTED_CA_FILE", c.FRPTrustedCAFile}, {"FRPS_CONFIG_FILE", c.FRPSConfigFile},
		{"OIDC_WEB_CLIENT_ID", c.OIDCWebClientID}, {"OIDC_WEB_CLIENT_SECRET", c.OIDCWebClientSecret}, {"OIDC_NATIVE_CLIENT_ID", c.OIDCNativeClientID},
		{"FRPS_ADMIN_URL", c.FRPSAdminURL}, {"FRPS_ADMIN_USERNAME", c.FRPSAdminUsername}, {"FRPS_ADMIN_PASSWORD", c.FRPSAdminPassword},
	} {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s is required", item.name)
		}
	}
	if c.OIDCWebClientID == c.OIDCNativeClientID {
		return errors.New("OIDC web and native client IDs must differ")
	}
	if !validHTTPSURL(c.PublicOrigin, true) {
		return errors.New("PUBLIC_ORIGIN must be an exact HTTPS origin")
	}
	if !validHTTPSURL(c.OIDCIssuer, false) {
		return errors.New("OIDC_ISSUER must be an exact HTTPS URL")
	}
	if c.OIDCResource != APIResource {
		return errors.New("OIDC resource must be the Tunnel API resource")
	}
	if !validDomain(c.PublicDomain) || !validHost(c.FRPServerAddr) || !validHost(c.FRPTLSServerName) {
		return errors.New("invalid public domain or FRP server identity")
	}
	if c.FRPServerPort < 1 || c.FRPServerPort > 65535 || c.RedisDB < 0 || c.RedisDB > 15 {
		return errors.New("invalid FRP_SERVER_PORT or REDIS_DB")
	}
	if port := c.frpsBindPort(); port < 1 || port > 65535 {
		return errors.New("invalid FRPS_BIND_PORT")
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return errors.New("LISTEN_ADDR must be a host:port address")
	}
	if !loopbackListener(c.PluginListenAddr) || c.PluginListenAddr == c.ListenAddr {
		return errors.New("FRP_PLUGIN_LISTEN_ADDR must be a separate literal-loopback host:port address")
	}
	if _, _, err := net.SplitHostPort(c.RedisAddr); err != nil {
		return errors.New("REDIS_ADDR must be a host:port address")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return errors.New("invalid LOG_LEVEL")
	}
	if !validPrefix(c.RedisPrefix) {
		return errors.New("invalid REDIS_PREFIX")
	}
	if c.TCPPortStart < 1024 || c.TCPPortStart > c.TCPPortEnd || c.TCPPortEnd > 65535 || c.UDPPortStart < 1024 || c.UDPPortStart > c.UDPPortEnd || c.UDPPortEnd > 65535 {
		return errors.New("invalid anonymous TCP or UDP port range")
	}
	secrets := [][]byte{c.LaunchPepper, c.RunPepper, c.ReplayKey, c.SessionKey, c.AnonymousPepper, c.AnonymousReplayKey, c.AnonymousFenceToken}
	for index, secret := range secrets {
		if len(secret) != 32 {
			return fmt.Errorf("%s must contain 32 bytes", secretConfigNames[index])
		}
		for previous := 0; previous < index; previous++ {
			if bytes.Equal(secret, secrets[previous]) {
				return errors.New("control, session, and anonymous secrets must all be distinct")
			}
		}
	}
	return nil
}

func (c Config) frpsBindPort() int {
	// An omitted bind port in programmatic configurations keeps the public-port default.
	if c.FRPSBindPort == 0 {
		return c.FRPServerPort
	}
	return c.FRPSBindPort
}

func loopbackListener(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	number, portErr := strconv.Atoi(port)
	return err == nil && ip.IsLoopback() && ip.Zone() == "" && portErr == nil && number > 0 && number <= 65535
}

func validHTTPSURL(raw string, origin bool) bool {
	parsed, err := url.Parse(raw)
	return err == nil && raw != "" && parsed.String() == raw && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.Opaque == "" &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && parsed.RawFragment == "" && parsed.RawPath == "" && (!origin || parsed.Path == "")
}

func validHost(raw string) bool {
	if ip, err := netip.ParseAddr(raw); err == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	return validDomain(raw)
}

func validDomain(raw string) bool {
	if len(raw) > 253 || !strings.Contains(raw, ".") || strings.ToLower(raw) != raw {
		return false
	}
	for _, part := range strings.Split(raw, ".") {
		if part == "" || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validPrefix(raw string) bool {
	if len(raw) > 170 || !strings.Contains(raw, ":") {
		return false
	}
	for _, part := range strings.Split(raw, ":") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}
