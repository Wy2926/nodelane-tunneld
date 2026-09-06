package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/cliauth"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
)

func defaultCommandDependencies(ui *consoleUI) commandDependencies {
	return commandDependencies{
		api: func() (commandAPI, error) { return runclient.New(runclient.Options{BaseURL: os.Getenv("NT_API_URL")}) },
		account: func(ctx context.Context, oidc runclient.OIDCConfig) (accountSession, error) {
			store, err := commandCredentialStore()
			if err != nil {
				return nil, err
			}
			return cliauth.New(ctx, cliauth.Options{Issuer: oidc.Issuer, ClientID: oidc.ClientID, Resource: oidc.Resource, Store: store})
		},
		logout: func(ctx context.Context) error {
			store, err := commandCredentialStore()
			if err != nil {
				return err
			}
			return logoutWithStore(ctx, store, ui)
		},
		openBrowser: openDeviceBrowser,
		preflight:   runclient.Preflight,
		validateBootstrap: func(config runclient.BootstrapConfig) error {
			if err := runclient.ValidateProxyURL(os.Getenv("NT_FRP_PROXY_URL")); err != nil {
				return err
			}
			return runclient.ValidateBootstrapConfig(config, os.Getenv("NT_CA_FILE"))
		},
		installationID: func() (string, error) {
			path := os.Getenv("NT_INSTALLATION_FILE")
			if path == "" {
				root, err := os.UserConfigDir()
				if err != nil {
					return "", runclient.ErrInvalidConfiguration
				}
				path = filepath.Join(root, "nodelane", "tunnel", "installation-id")
			}
			return runclient.LoadOrCreateInstallationID(path)
		},
		requestKey: runclient.NewRequestKey,
		run: func(ctx context.Context, api commandAPI, config runclient.BootstrapConfig, run runclient.Run, target runclient.Target) error {
			ui.detail(ui.text(msgRunIDLabel), safeConsoleField(run.ID, 64))
			ui.detail(ui.text(msgLocalAddressLabel), net.JoinHostPort(target.LocalHost, strconv.Itoa(target.LocalPort)))
			if !run.HardExpiresAt.IsZero() {
				ui.detail(ui.text(msgExpiresAtLabel), run.HardExpiresAt.Local().Format("2006-01-02 15:04:05 MST"))
			}
			runner, err := runclient.NewRunner(runclient.RunnerOptions{
				Backend: api, CAFile: os.Getenv("NT_CA_FILE"), ProxyURL: os.Getenv("NT_FRP_PROXY_URL"),
				OnStatus: func(status runclient.Status) {
					switch status.State {
					case runclient.StatusConnecting:
						ui.step(ui.text(msgConnectingEdge))
					case runclient.StatusOnline:
						ui.success(ui.text(msgTunnelConnected))
						ui.highlightedDetail(ui.text(msgPublicAddressLabel), safeConsoleField(status.PublicEndpoint, 2048))
					case runclient.StatusReconnecting:
						ui.warning(ui.text(msgRunReconnecting))
					case runclient.StatusStopping:
						ui.step(ui.text(msgRunStopping))
					case runclient.StatusStopped:
						ui.success(ui.text(msgRunStopped))
					}
				},
			})
			if err != nil {
				return err
			}
			err = runner.Run(ctx, config, run, target)
			if errors.Is(err, runclient.ErrRunStopped) {
				return nil
			}
			return err
		},
	}
}

func logoutWithStore(ctx context.Context, store cliauth.Store, ui *consoleUI) error {
	err := cliauth.LogoutStored(ctx, store)
	if errors.Is(err, cliauth.ErrRevocationUnconfirmed) && !errors.Is(err, cliauth.ErrCredentialsUnavailable) {
		ui.success(ui.text(msgLoggedOut))
	}
	return err
}

func commandCredentialStore() (cliauth.Store, error) {
	base := os.Getenv("NT_API_URL")
	if base == "" {
		base = runclient.DefaultBaseURL
	}
	if _, err := runclient.New(runclient.Options{BaseURL: base}); err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(strings.TrimRight(base, "/"))
	parsed.Host = strings.ToLower(parsed.Host)
	digest := sha256.Sum256([]byte(parsed.String()))
	account := hex.EncodeToString(digest[:])
	switch os.Getenv("NT_ACCOUNT_STORE") {
	case "", "keyring":
		return cliauth.NewSystemStore(cliauth.SystemStoreOptions{Service: "net.nodelane.nt", Account: account})
	case "file":
		path := os.Getenv("NT_ACCOUNT_CREDENTIALS_FILE")
		if path == "" {
			root, err := os.UserConfigDir()
			if err != nil {
				return nil, cliauth.ErrCredentialsUnavailable
			}
			path = filepath.Join(root, "nodelane", "tunnel", "accounts", account, "credentials.json")
		}
		return cliauth.NewFileStore(path)
	default:
		return nil, cliauth.ErrInvalidConfiguration
	}
}
