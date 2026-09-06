package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/controlserver"
	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	"gopkg.in/yaml.v2"
)

var requiredSecrets = []string{"CONTROL_LAUNCH_PEPPER", "CONTROL_RUN_PEPPER", "CONTROL_REPLAY_KEY", "SESSION_ENCRYPTION_KEY", "ANONYMOUS_CREDENTIAL_PEPPER", "ANONYMOUS_REPLAY_KEY", "ANONYMOUS_FENCE_OWNER_TOKEN"}

func deploymentValues() map[string]string {
	values := map[string]string{
		"FRP_SERVER_PORT": "7000", "PUBLIC_DOMAIN": "tunnel.nodelane.net", "TCP_PORT_START": "20000", "TCP_PORT_END": "29999", "UDP_PORT_START": "30000", "UDP_PORT_END": "39999",
		"FRPS_CERT_FILE": "/etc/nodelane/certs/frps.crt", "FRPS_KEY_FILE": "/etc/nodelane/certs/frps.key", "FRPS_CONFIG_FILE": "/etc/nodelane/frps.toml", "FRP_TRUSTED_CA_FILE": "/etc/nodelane/certs/frps-ca.crt",
		"FRPS_ADMIN_USERNAME": "fixture-operator", "FRPS_ADMIN_PASSWORD": "fixture-\"quoted\"-password", "DATABASE_URL": "postgres://fixture:fixture@127.0.0.1:5432/fixture?sslmode=disable", "REDIS_ADDR": "127.0.0.1:6379",
		"OIDC_WEB_CLIENT_ID": "fixture-web", "OIDC_WEB_CLIENT_SECRET": "fixture-private", "OIDC_NATIVE_CLIENT_ID": "fixture-native", "TUNNELD_IMAGE": "example.invalid/nodelane/tunneld:fixture",
	}
	for index, key := range requiredSecrets {
		values[key] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(index + 1)}, 32))
	}
	return values
}

func TestDeploymentUsesStockPrivatePluginAuthorization(t *testing.T) {
	source, err := os.ReadFile("frps.toml")
	if err != nil {
		t.Fatal(err)
	}
	values := deploymentValues()
	rendered, err := frpconfig.RenderWithTemplate(source, &frpconfig.Values{Envs: values})
	if err != nil {
		t.Fatal(err)
	}
	var cfg v1.ServerConfig
	if err := frpconfig.LoadConfigure(rendered, &cfg, true, "toml"); err != nil {
		t.Fatal(err)
	}
	cfg.Complete()
	validator := validation.NewConfigValidator(security.NewUnsafeFeatures(nil))
	if _, err := validator.ValidateServerConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Method != v1.AuthMethodToken || cfg.Auth.Token != "" || cfg.Auth.TokenSource != nil {
		t.Fatal("stock token auth must be empty and have no token source")
	}
	if !slices.Contains(cfg.Auth.AdditionalScopes, v1.AuthScopeHeartBeats) || !slices.Contains(cfg.Auth.AdditionalScopes, v1.AuthScopeNewWorkConns) {
		t.Fatal("heartbeat/work connection auth scopes are required")
	}
	if !cfg.Transport.TLS.Force || cfg.Transport.HeartbeatTimeout != 45 || cfg.Transport.TLS.TrustedCaFile != "" {
		t.Fatal("server TLS and finite control heartbeat are required without client-certificate auth")
	}
	if !path.IsAbs(cfg.Transport.TLS.CertFile) || cfg.Transport.TLS.CertFile != values["FRPS_CERT_FILE"] || cfg.Transport.TLS.KeyFile != values["FRPS_KEY_FILE"] {
		t.Fatal("configured absolute certificate paths do not match mounted paths")
	}
	if cfg.BindPort != 7000 || cfg.VhostHTTPPort != 8080 || cfg.SubDomainHost != values["PUBLIC_DOMAIN"] {
		t.Fatal("public listener settings do not match the control plane")
	}
	if cfg.WebServer.Addr != "127.0.0.1" || cfg.WebServer.Port != 7500 || cfg.WebServer.User != values["FRPS_ADMIN_USERNAME"] || cfg.WebServer.Password != values["FRPS_ADMIN_PASSWORD"] || cfg.EnablePrometheus {
		t.Fatal("native admin snapshots must use matching credentials and private loopback without Prometheus")
	}
	if len(cfg.HTTPPlugins) != 1 {
		t.Fatal("exactly one authorization plugin is required")
	}
	plugin := cfg.HTTPPlugins[0]
	if plugin.Addr != "127.0.0.1:9001" || plugin.Path != "/internal/frp" || !reflect.DeepEqual(plugin.Ops, []string{"Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"}) {
		t.Fatal("all six operations must use the separate private plugin listener")
	}
}

func TestDeploymentFRPSBindPortIsIndependentOfPublicPort(t *testing.T) {
	source, err := os.ReadFile("frps.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, public, bind string
		want               int
	}{
		{"default", "7000", "", 7000},
		{"nondefault fallback", "7001", "", 7001},
		{"TCP forwarded", "7001", "7000", 7000},
		{"minimum internal", "7001", "1", 1},
		{"maximum internal", "7001", "65535", 65535},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := deploymentValues()
			values["FRP_SERVER_PORT"] = test.public
			if test.bind != "" {
				values["FRPS_BIND_PORT"] = test.bind
			}
			rendered, err := frpconfig.RenderWithTemplate(source, &frpconfig.Values{Envs: values})
			if err != nil {
				t.Fatal(err)
			}
			var cfg v1.ServerConfig
			if err := frpconfig.LoadConfigure(rendered, &cfg, true, "toml"); err != nil {
				t.Fatal(err)
			}
			if cfg.BindPort != test.want {
				t.Fatalf("frps bindPort=%d, want %d", cfg.BindPort, test.want)
			}
		})
	}
}

type composeService struct {
	Image       string            `yaml:"image"`
	Network     string            `yaml:"network_mode"`
	ReadOnly    bool              `yaml:"read_only"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []struct {
		Source   string `yaml:"source"`
		Target   string `yaml:"target"`
		ReadOnly bool   `yaml:"read_only"`
		Bind     struct {
			CreateHostPath bool `yaml:"create_host_path"`
		} `yaml:"bind"`
	} `yaml:"volumes"`
}

func TestDeploymentMountsPrivateKeyOnlyIntoFRPS(t *testing.T) {
	data, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 2 || cfg.Services["frps"].Image != "ghcr.io/fatedier/frps:v0.70.0" {
		t.Fatal("fresh deployment must include the pinned stock frps and tunneld")
	}
	for name, service := range cfg.Services {
		if service.Network != "host" || !service.ReadOnly || len(service.Ports) != 0 || service.Environment["TZ"] != "UTC" {
			t.Fatalf("%s must preserve read-only host networking and UTC", name)
		}
		keyMounts := 0
		for _, volume := range service.Volumes {
			if volume.Source != volume.Target || !volume.ReadOnly || volume.Bind.CreateHostPath {
				t.Fatalf("%s changed a same-path read-only existing-file mount", name)
			}
			if strings.Contains(volume.Target, "FRPS_KEY_FILE") {
				keyMounts++
			}
		}
		if (name == "frps" && (keyMounts != 1 || len(service.Volumes) != 4)) || (name == "tunneld" && (keyMounts != 0 || len(service.Volumes) != 3)) {
			t.Fatalf("%s has incorrect public/private certificate mounts", name)
		}
		if _, exists := service.Environment["FRP_AUTH_TOKEN"]; exists {
			t.Fatal("shared auth token must not be passed to either process")
		}
		if service.Environment["FRP_SERVER_PORT"] != "${FRP_SERVER_PORT:-7000}" || service.Environment["FRPS_BIND_PORT"] != "${FRPS_BIND_PORT:-}" {
			t.Fatal("public and optional internal FRP ports must have matching explicit defaults")
		}
	}
	control := cfg.Services["tunneld"].Environment
	if control["LISTEN_ADDR"] != "127.0.0.1:9000" || control["FRP_PLUGIN_LISTEN_ADDR"] != "127.0.0.1:9001" || control["FRPS_ADMIN_URL"] != "http://127.0.0.1:7500" {
		t.Fatal("public API, plugin, and native admin listeners must remain separate")
	}
	for _, name := range append(requiredSecrets, "DATABASE_URL", "REDIS_ADDR", "OIDC_WEB_CLIENT_ID", "OIDC_WEB_CLIENT_SECRET", "OIDC_NATIVE_CLIENT_ID", "FRPS_ADMIN_USERNAME", "FRPS_ADMIN_PASSWORD") {
		if !strings.HasPrefix(control[name], "${"+name+":?") {
			t.Fatalf("%s must fail closed when missing", name)
		}
	}
	for name, value := range cfg.Services["frps"].Environment {
		if control[name] != value {
			t.Fatalf("frps template input %s differs between processes", name)
		}
	}
}

func TestDeploymentComposeKeepsFRPPortInputsAligned(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker Compose CLI unavailable")
	}
	file := filepath.Join(t.TempDir(), "empty.env")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("frps.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, public, bind string
		wantPublic         int
		wantBind           int
	}{
		{"defaults", "", "", 7000, 7000},
		{"nondefault fallback", "7001", "", 7001, 7001},
		{"TCP forwarded", "7001", "7000", 7001, 7000},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := deploymentValues()
			values["FRP_SERVER_PORT"], values["FRPS_BIND_PORT"] = test.public, test.bind
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "docker", "compose", "--env-file", file, "-f", "compose.yaml", "-f", "compose.registry.yaml", "config", "--format", "json")
			command.Env = os.Environ()
			for name, value := range values {
				command.Env = append(command.Env, name+"="+value)
			}
			output, err := command.Output()
			if err != nil {
				t.Fatalf("Compose port configuration failed: %v", err)
			}
			var cfg struct {
				Services map[string]struct {
					Environment map[string]string `json:"environment"`
				} `json:"services"`
			}
			if err := json.Unmarshal(output, &cfg); err != nil {
				t.Fatal("Compose returned invalid structured configuration")
			}
			control := cfg.Services["tunneld"].Environment
			for _, name := range []string{"FRP_SERVER_PORT", "FRPS_BIND_PORT"} {
				value, present := control[name]
				stockValue, stockPresent := cfg.Services["frps"].Environment[name]
				if !present || !stockPresent || value != stockValue {
					t.Fatal("Compose gave the two processes different FRP port inputs")
				}
			}
			for name, value := range control {
				t.Setenv(name, value)
			}
			t.Setenv("FRP_AUTH_TOKEN", "")
			controlConfig, err := controlserver.LoadConfig()
			if err != nil || controlConfig.FRPServerPort != test.wantPublic || controlConfig.FRPSBindPort != test.wantBind {
				t.Fatalf("Compose control-plane port parsing disagrees with configured defaults: %v", err)
			}
			for _, name := range []string{"tunneld", "frps"} {
				rendered, err := frpconfig.RenderWithTemplate(source, &frpconfig.Values{Envs: cfg.Services[name].Environment})
				if err != nil {
					t.Fatal("Compose environment did not render the shared frps template")
				}
				var stock v1.ServerConfig
				if err := frpconfig.LoadConfigure(rendered, &stock, true, "toml"); err != nil || stock.BindPort != controlConfig.FRPSBindPort {
					t.Fatal("Compose frps template disagrees with the control-plane internal listener")
				}
			}
		})
	}
}

func TestDeploymentComposeValidatesWithoutRenderingSecrets(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker Compose CLI unavailable")
	}
	file := filepath.Join(t.TempDir(), "empty.env")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, registry := range []bool{false, true} {
		args := []string{"compose", "--env-file", file, "-f", "compose.yaml"}
		if registry {
			args = append(args, "-f", "compose.registry.yaml")
		}
		args = append(args, "config", "--quiet")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		command := exec.CommandContext(ctx, "docker", args...)
		command.Env = os.Environ()
		for name, value := range deploymentValues() {
			command.Env = append(command.Env, name+"="+value)
		}
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("Compose validation registry=%v: %v: %s", registry, err, output)
		}
		if len(bytes.TrimSpace(output)) != 0 {
			t.Fatal("quiet validation unexpectedly produced output")
		}
	}
}
