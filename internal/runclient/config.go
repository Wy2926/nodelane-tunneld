package runclient

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/pelletier/go-toml/v2"
)

const maxCABytes = 256 << 10

// ValidateProxyURL rejects upstream proxy negotiation because the pinned frp
// connector does not expose a cancellable socket until that negotiation ends.
func ValidateProxyURL(raw string) error {
	if raw != "" {
		return ErrProxyUnsupported
	}
	return nil
}

// ValidateBootstrapConfig is safe to call before allocating a run. Anonymous
// deployments may omit OIDC entirely; partial account configuration is invalid.
func ValidateBootstrapConfig(config BootstrapConfig, caFile string) error {
	if err := validateBootstrapMetadata(config); err != nil {
		return err
	}
	_, err := trustedCA(config, caFile)
	return err
}

func validateBootstrapMetadata(config BootstrapConfig) error {
	if !validHost(config.FRP.ServerAddr) || config.FRP.ServerPort < 1 || config.FRP.ServerPort > 65535 || !validHost(config.FRP.TLSServerName) || strings.Contains(config.FRP.TLSServerName, "%") {
		return ErrInvalidConfiguration
	}
	oidc := config.OIDC
	if oidc != (OIDCConfig{}) {
		issuer, err := url.Parse(oidc.Issuer)
		if err != nil || issuer.User != nil || issuer.RawQuery != "" || issuer.ForceQuery || issuer.Fragment != "" || !validHost(issuer.Hostname()) || !validOpaque(oidc.ClientID, 256) || !validOpaque(oidc.Resource, 2048) {
			return ErrInvalidConfiguration
		}
		if issuer.Scheme != "https" {
			ip, err := netip.ParseAddr(issuer.Hostname())
			if issuer.Scheme != "http" || err != nil || !ip.IsLoopback() {
				return ErrInvalidConfiguration
			}
		}
	}
	return nil
}

func trustedCA(config BootstrapConfig, caFile string) ([]byte, error) {
	data := []byte(config.FRP.TrustedCAPEM)
	if caFile != "" {
		file, err := os.Open(caFile)
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
		data, err = io.ReadAll(io.LimitReader(file, maxCABytes+1))
		_ = file.Close()
		if err != nil {
			return nil, ErrInvalidConfiguration
		}
	}
	if len(data) == 0 || len(data) > maxCABytes {
		return nil, ErrInvalidConfiguration
	}
	remaining := bytes.TrimSpace(data)
	count := 0
	now := time.Now()
	for len(remaining) > 0 {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, ErrInvalidConfiguration
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, ErrInvalidConfiguration
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			return nil, ErrInvalidConfiguration
		}
		count++
		remaining = bytes.TrimSpace(rest)
	}
	if count == 0 {
		return nil, ErrInvalidConfiguration
	}
	return data, nil
}

func buildFRPConfig(bootstrap BootstrapConfig, run Run, target Target, caPath, credentialPath, proxyURL string) (string, error) {
	if err := ValidateProxyURL(proxyURL); err != nil {
		return "", err
	}
	if !validTarget(target) || target.Protocol != run.Protocol || !validAllocatedRun(run) || caPath == "" || credentialPath == "" {
		return "", ErrInvalidConfiguration
	}
	enabled, disabled := true, false
	common := v1.ClientCommonConfig{
		ServerAddr: bootstrap.FRP.ServerAddr, ServerPort: bootstrap.FRP.ServerPort, ClientID: run.ID, LoginFailExit: &disabled,
		Auth: v1.AuthClientConfig{Method: v1.AuthMethodOIDC, Token: "", AdditionalScopes: []v1.AuthScope{v1.AuthScopeHeartBeats, v1.AuthScopeNewWorkConns},
			OIDC: v1.AuthOIDCClientConfig{TokenSource: &v1.ValueSource{Type: "file", File: &v1.FileSource{Path: credentialPath}}}},
		Metadatas: map[string]string{frpplugin.MetadataRunID: run.ID, frpplugin.MetadataRunToken: run.CredentialToken},
		Log:       v1.LogConfig{To: "console", Level: "error", DisablePrintColor: true},
		Transport: v1.ClientTransportConfig{Protocol: "tcp", ProxyURL: proxyURL, TCPMux: &enabled, TCPMuxKeepaliveInterval: 5, HeartbeatInterval: 5, HeartbeatTimeout: 15, DialServerTimeout: 5,
			TLS: v1.TLSClientConfig{Enable: &enabled, TLSConfig: v1.TLSConfig{TrustedCaFile: caPath, ServerName: bootstrap.FRP.TLSServerName}}},
	}
	base := v1.ProxyBaseConfig{Name: run.ProxyName, Type: run.Protocol, ProxyBackend: v1.ProxyBackend{LocalIP: target.LocalHost, LocalPort: target.LocalPort}}
	var proxy v1.ProxyConfigurer
	if run.Protocol == "http" {
		subdomain := run.Subdomain
		if subdomain == "" {
			subdomain, _, _ = strings.Cut(run.PublicEndpoint, ".")
		}
		if !validHost(subdomain) || strings.Contains(subdomain, ".") {
			return "", ErrInvalidConfiguration
		}
		proxy = &v1.HTTPProxyConfig{ProxyBaseConfig: base, DomainConfig: v1.DomainConfig{SubDomain: subdomain}}
	} else {
		_, rawPort, err := net.SplitHostPort(run.PublicEndpoint)
		if err != nil {
			return "", ErrInvalidConfiguration
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return "", ErrInvalidConfiguration
		}
		if run.Protocol == "tcp" {
			proxy = &v1.TCPProxyConfig{ProxyBaseConfig: base, RemotePort: port}
		} else {
			proxy = &v1.UDPProxyConfig{ProxyBaseConfig: base, RemotePort: port}
		}
	}
	config := v1.ClientConfig{ClientCommonConfig: common, Proxies: []v1.TypedProxyConfig{{Type: run.Protocol, ProxyConfigurer: proxy}}}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", ErrInvalidConfiguration
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return "", ErrInvalidConfiguration
	}
	// Upstream types define JSON names. Preserve those names and integer types
	// while using the maintained TOML encoder for escaping and table layout.
	data, err := toml.Marshal(integerJSONValues(object))
	if err != nil {
		return "", ErrInvalidConfiguration
	}
	return string(data), nil
}

func integerJSONValues(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = integerJSONValues(child)
		}
	case []any:
		for index, child := range typed {
			typed[index] = integerJSONValues(child)
		}
	case float64:
		if typed == math.Trunc(typed) && typed >= math.MinInt64 && typed < math.MaxInt64 {
			return int64(typed)
		}
	}
	return value
}
