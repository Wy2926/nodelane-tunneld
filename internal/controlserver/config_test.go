package controlserver

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func configEnvironment() map[string]string {
	values := map[string]string{
		"DATABASE_URL":           "postgres://operator:database-secret@127.0.0.1/control",
		"REDIS_ADDR":             "127.0.0.1:6379",
		"FRP_TRUSTED_CA_FILE":    "operator-ca.pem",
		"FRPS_CONFIG_FILE":       "operator-frps.toml",
		"OIDC_WEB_CLIENT_ID":     "web-client",
		"OIDC_WEB_CLIENT_SECRET": "private-web-secret",
		"OIDC_NATIVE_CLIENT_ID":  "native-client",
		"FRPS_ADMIN_URL":         "http://127.0.0.1:7500",
		"FRPS_ADMIN_USERNAME":    "operator",
		"FRPS_ADMIN_PASSWORD":    "private-admin-secret",
	}
	for index, name := range secretConfigNames {
		values[name] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(index + 1)}, 32))
	}
	return values
}

func TestConfigurationRequiresExternalStoresEvenInDevelopment(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "REDIS_ADDR"} {
		t.Run(name, func(t *testing.T) {
			values := configEnvironment()
			values["DEV_MODE"] = "true"
			delete(values, name)
			_, err := parseConfig(func(key string) string { return values[key] })
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("missing %s did not fail closed: %v", name, err)
			}
		})
	}
}

func TestConfigurationRejectsPublicPluginListenersAndSecretReuse(t *testing.T) {
	for _, address := range []string{":9001", "0.0.0.0:9001", "[::]:9001", "localhost:9001", "192.0.2.1:9001"} {
		values := configEnvironment()
		values["FRP_PLUGIN_LISTEN_ADDR"] = address
		if _, err := parseConfig(func(key string) string { return values[key] }); err == nil {
			t.Errorf("accepted plugin listener %q", address)
		}
	}
	values := configEnvironment()
	values["ANONYMOUS_REPLAY_KEY"] = values["SESSION_ENCRYPTION_KEY"]
	if _, err := parseConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("accepted shared session/anonymous replay key")
	}
}

func TestConfigurationRejectsLegacyFRPSharedToken(t *testing.T) {
	values := configEnvironment()
	values["FRP_AUTH_TOKEN"] = "legacy-private-token"
	_, err := parseConfig(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "FRP_AUTH_TOKEN") || strings.Contains(err.Error(), values["FRP_AUTH_TOKEN"]) {
		t.Fatalf("legacy shared-token mode not rejected safely: %v", err)
	}
}

func TestConfigurationErrorsDoNotEchoValues(t *testing.T) {
	for _, name := range []string{"CONTROL_REPLAY_KEY", "FRP_SERVER_PORT", "FRPS_BIND_PORT", "TCP_PORT_START", "TRUSTED_PROXY_CIDRS", "OIDC_ISSUER"} {
		values := configEnvironment()
		values[name] = "sensitive-invalid-value"
		_, err := parseConfig(func(key string) string { return values[key] })
		if err == nil || strings.Contains(err.Error(), values[name]) {
			t.Errorf("invalid %s was accepted or echoed: %v", name, err)
		}
	}
}

func TestConfigurationRejectsInvalidFRPSBindPort(t *testing.T) {
	for _, raw := range []string{"0", "-1", "65536", "7000.5", " 7000 ", "sensitive-invalid-value", "999999999999999999999999999999"} {
		t.Run(raw, func(t *testing.T) {
			values := configEnvironment()
			values["FRPS_BIND_PORT"] = raw
			_, err := parseConfig(func(key string) string { return values[key] })
			if err == nil || !strings.Contains(err.Error(), "FRPS_BIND_PORT") || strings.Contains(err.Error(), raw) {
				t.Fatalf("invalid internal port was accepted or echoed: %v", err)
			}
		})
	}
}

func TestConfigurationSeparatesPublicAndInternalFRPPorts(t *testing.T) {
	for _, test := range []struct {
		name         string
		ports        map[string]string
		public, bind int
	}{
		{"defaults", nil, 7000, 7000},
		{"nondefault public", map[string]string{"FRP_SERVER_PORT": "7001"}, 7001, 7001},
		{"empty internal", map[string]string{"FRP_SERVER_PORT": "7001", "FRPS_BIND_PORT": ""}, 7001, 7001},
		{"TCP forwarded", map[string]string{"FRP_SERVER_PORT": "7001", "FRPS_BIND_PORT": "7000"}, 7001, 7000},
		{"minimum internal", map[string]string{"FRPS_BIND_PORT": "1"}, 7000, 1},
		{"maximum internal", map[string]string{"FRPS_BIND_PORT": "65535"}, 7000, 65535},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := configEnvironment()
			for name, value := range test.ports {
				values[name] = value
			}
			cfg, err := parseConfig(func(key string) string { return values[key] })
			if err != nil {
				t.Fatal(err)
			}
			if cfg.FRPServerPort != test.public || cfg.FRPSBindPort != test.bind {
				t.Fatalf("public/bind ports=%d/%d, want %d/%d", cfg.FRPServerPort, cfg.FRPSBindPort, test.public, test.bind)
			}
		})
	}
}

func TestConfigurationValidatesProgrammaticFRPSBindPort(t *testing.T) {
	values := configEnvironment()
	cfg, err := parseConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.FRPServerPort, cfg.FRPSBindPort = 7001, 0
	if err := cfg.Validate(); err != nil || cfg.frpsBindPort() != 7001 {
		t.Fatalf("omitted programmatic bind port did not use public port: %v", err)
	}
	for _, port := range []int{-1, 65536} {
		cfg.FRPSBindPort = port
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "FRPS_BIND_PORT") {
			t.Fatalf("invalid programmatic bind port accepted: %v", err)
		}
	}
}

func TestConfigurationDefaultsPublishOnlyConfiguredNativeIdentity(t *testing.T) {
	values := configEnvironment()
	cfg, err := parseConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDCIssuer != "https://auth.nodelane.net/oidc" || cfg.OIDCResource != "https://tunnel.nodelane.net/api" || cfg.OIDCNativeClientID != "native-client" || cfg.PluginListenAddr != "127.0.0.1:9001" {
		t.Fatal("invalid public identity or private listener defaults")
	}
	delete(values, "OIDC_NATIVE_CLIENT_ID")
	if _, err := parseConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("invented a native client identity")
	}
}
