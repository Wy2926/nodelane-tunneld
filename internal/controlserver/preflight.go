package controlserver

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
)

const maxOperatorFileBytes = 1 << 20

func preflight(cfg Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	caPEM, err := readOperatorFile(cfg.FRPTrustedCAFile)
	if err != nil {
		return "", errors.New("FRP_TRUSTED_CA_FILE must be a readable bounded public certificate file")
	}
	certificates, err := publicCertificates(caPEM)
	if err != nil {
		return "", errors.New("FRP_TRUSTED_CA_FILE must contain only valid CA certificates")
	}
	pool := x509.NewCertPool()
	for _, cert := range certificates {
		if !cert.IsCA || !cert.BasicConstraintsValid {
			return "", errors.New("FRP_TRUSTED_CA_FILE must contain only CA certificates")
		}
		pool.AddCert(cert)
	}
	if _, err := readOperatorFile(cfg.FRPSConfigFile); err != nil {
		return "", errors.New("FRPS_CONFIG_FILE must be a readable bounded file")
	}
	stock, legacy, err := frpconfig.LoadServerConfig(cfg.FRPSConfigFile, true)
	if err != nil || legacy || stock == nil {
		return "", errors.New("FRPS_CONFIG_FILE must contain valid stock frps configuration")
	}
	validator := validation.NewConfigValidator(security.NewUnsafeFeatures(nil))
	if _, err := validator.ValidateServerConfig(stock); err != nil {
		return "", errors.New("FRPS_CONFIG_FILE failed stock configuration validation")
	}
	if stock.Auth.Method != v1.AuthMethodToken || stock.Auth.Token != "" || stock.Auth.TokenSource != nil {
		return "", errors.New("FRPS_CONFIG_FILE must use empty stock token auth without a token source")
	}
	if stock.EnablePrometheus {
		return "", errors.New("FRPS_CONFIG_FILE must disable Prometheus in native snapshot mode")
	}
	if !slices.Contains(stock.Auth.AdditionalScopes, v1.AuthScopeHeartBeats) || !slices.Contains(stock.Auth.AdditionalScopes, v1.AuthScopeNewWorkConns) {
		return "", errors.New("FRPS_CONFIG_FILE must authorize heartbeats and work connections")
	}
	if !stock.Transport.TLS.Force || stock.Transport.TLS.TrustedCaFile != "" || stock.Transport.TLS.KeyFile == "" || !filepath.IsAbs(stock.Transport.TLS.CertFile) {
		return "", errors.New("FRPS_CONFIG_FILE must force server TLS with an absolute public certificate path and no client-certificate requirement")
	}
	serverPEM, err := readOperatorFile(stock.Transport.TLS.CertFile)
	if err != nil {
		return "", errors.New("frps public server certificate is unavailable")
	}
	serverCertificates, err := publicCertificates(serverPEM)
	if err != nil {
		return "", errors.New("frps public server certificate is invalid")
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range serverCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	if _, err := serverCertificates[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediates, DNSName: cfg.FRPTLSServerName}); err != nil {
		return "", errors.New("frps server certificate does not validate against the public CA and FRP_TLS_SERVER_NAME")
	}
	if stock.Transport.HeartbeatTimeout <= 0 || stock.Transport.HeartbeatTimeout > 90 {
		return "", errors.New("FRPS_CONFIG_FILE must set a positive heartbeat timeout of at most 90 seconds")
	}
	if stock.BindPort != cfg.FRPServerPort || stock.SubDomainHost != cfg.PublicDomain || stock.VhostHTTPPort < 1 {
		return "", errors.New("FRPS_CONFIG_FILE public port, HTTP listener, or subdomain host does not match")
	}
	if stock.SSHTunnelGateway.BindPort != 0 {
		return "", errors.New("FRPS_CONFIG_FILE SSH tunnel gateway must be disabled")
	}
	if len(stock.HTTPPlugins) != 1 {
		return "", errors.New("FRPS_CONFIG_FILE must configure exactly the Tunnel authorization plugin")
	}
	plugin := stock.HTTPPlugins[0]
	pluginAddress := plugin.Addr
	if !strings.Contains(pluginAddress, "://") {
		pluginAddress = "http://" + pluginAddress
	}
	if pluginAddress != "http://"+cfg.PluginListenAddr || plugin.Path != "/internal/frp" || len(plugin.Ops) != 6 {
		return "", errors.New("FRPS_CONFIG_FILE plugin must target the private Tunnel listener with all six operations")
	}
	for _, operation := range []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"} {
		if !slices.Contains(plugin.Ops, operation) {
			return "", errors.New("FRPS_CONFIG_FILE is missing a required plugin operation")
		}
	}
	if _, err := frpevidence.NewClient(frpevidence.Options{Endpoint: cfg.FRPSAdminURL, Username: cfg.FRPSAdminUsername, Password: cfg.FRPSAdminPassword}); err != nil {
		return "", errors.New("FRPS_ADMIN_URL must be HTTPS or literal loopback HTTP with configured credentials")
	}
	admin, _ := url.Parse(cfg.FRPSAdminURL)
	port := admin.Port()
	if port == "" {
		if admin.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if strconv.Itoa(stock.WebServer.Port) != port || stock.WebServer.User != cfg.FRPSAdminUsername || stock.WebServer.Password != cfg.FRPSAdminPassword ||
		(stock.WebServer.TLS != nil) != (admin.Scheme == "https") {
		return "", errors.New("FRPS_CONFIG_FILE admin API does not match FRPS_ADMIN configuration")
	}
	return string(caPEM), nil
}

func readOperatorFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxOperatorFileBytes {
		return nil, errors.New("invalid operator file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxOperatorFileBytes+1))
	if err != nil || len(data) > maxOperatorFileBytes {
		return nil, errors.New("invalid operator file")
	}
	return data, nil
}

func publicCertificates(data []byte) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	for len(bytes.TrimSpace(data)) > 0 {
		data = bytes.TrimSpace(data)
		if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("certificate PEM required")
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("certificate PEM required")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid certificate")
		}
		certificates = append(certificates, certificate)
		data = rest
	}
	if len(certificates) == 0 {
		return nil, errors.New("certificate required")
	}
	return certificates, nil
}
