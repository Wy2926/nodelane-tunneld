package client

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

var Version = "0.1.0-dev"

const EmbeddedFRPVersion = "0.70.0"

// FRPConfig contains the values used to configure the embedded frp client.
type FRPConfig struct {
	ClientID  string
	Tunnel    Tunnel
	LocalPort int
	CAFile    string
	ProxyURL  string
	LogLevel  string
}

func (config FRPConfig) TOML() string {
	var builder strings.Builder
	q := strconv.Quote
	tunnel := config.Tunnel
	fmt.Fprintf(&builder, "serverAddr = %s\n", q(tunnel.FRP.ServerAddr))
	fmt.Fprintf(&builder, "serverPort = %d\n", tunnel.FRP.ServerPort)
	fmt.Fprintf(&builder, "clientID = %s\n", q(config.ClientID))
	fmt.Fprintf(&builder, "auth.method = \"token\"\n")
	fmt.Fprintf(&builder, "auth.token = %s\n", q(tunnel.FRP.AuthToken))
	fmt.Fprintf(&builder, "auth.additionalScopes = [\"HeartBeats\", \"NewWorkConns\"]\n")
	fmt.Fprintf(&builder, "transport.protocol = \"tcp\"\n")
	fmt.Fprintf(&builder, "transport.wireProtocol = \"v1\"\n")
	fmt.Fprintf(&builder, "transport.tcpMux = true\n")
	if config.ProxyURL != "" {
		fmt.Fprintf(&builder, "transport.proxyURL = %s\n", q(config.ProxyURL))
	}
	fmt.Fprintf(&builder, "transport.tls.enable = true\n")
	fmt.Fprintf(&builder, "transport.tls.disableCustomTLSFirstByte = true\n")
	if config.CAFile != "" {
		if tunnel.FRP.TLSServerName != "" {
			fmt.Fprintf(&builder, "transport.tls.serverName = %s\n", q(tunnel.FRP.TLSServerName))
		}
		fmt.Fprintf(&builder, "transport.tls.trustedCaFile = %s\n", q(filepath.ToSlash(config.CAFile)))
	}
	logLevel := strings.ToLower(config.LogLevel)
	if logLevel != "trace" && logLevel != "debug" && logLevel != "warn" && logLevel != "error" {
		logLevel = "info"
	}
	fmt.Fprintf(&builder, "log.to = \"console\"\n")
	fmt.Fprintf(&builder, "log.level = %s\n", q(logLevel))
	fmt.Fprintf(&builder, "log.disablePrintColor = true\n")
	fmt.Fprintf(&builder, "metadatas.tunnel_token = %s\n\n", q(tunnel.TunnelToken))
	fmt.Fprintf(&builder, "[[proxies]]\n")
	fmt.Fprintf(&builder, "name = %s\n", q(tunnel.ProxyName))
	fmt.Fprintf(&builder, "type = %s\n", q(tunnel.Protocol))
	fmt.Fprintf(&builder, "localIP = \"127.0.0.1\"\n")
	fmt.Fprintf(&builder, "localPort = %d\n", config.LocalPort)
	if tunnel.Protocol == "http" {
		fmt.Fprintf(&builder, "subdomain = %s\n", q(tunnel.Subdomain))
	} else {
		fmt.Fprintf(&builder, "remotePort = %d\n", tunnel.RemotePort)
	}
	if tunnel.BandwidthLimit != "" {
		fmt.Fprintf(&builder, "transport.bandwidthLimit = %s\n", q(tunnel.BandwidthLimit))
		fmt.Fprintf(&builder, "transport.bandwidthLimitMode = \"server\"\n")
	}
	fmt.Fprintf(&builder, "metadatas.session_id = %s\n", q(tunnel.ID))
	return builder.String()
}
