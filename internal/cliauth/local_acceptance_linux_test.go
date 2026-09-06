package cliauth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
	"golang.org/x/sys/unix"
)

// Opt-in only: this consumes and revokes the freshly authorized isolated CLI login.
func TestLocalDeviceCredentialRefreshAndLogout(t *testing.T) {
	if os.Getenv("NODELANE_LOCAL_IDENTITY_ACCEPTANCE") != "1" {
		t.Skip("explicit isolated local identity acceptance is required")
	}
	const credentialPath = "/cli-account/credentials.json"
	const issuer = "https://localhost:3443/oidc"
	const resource = "https://tunnel.nodelane.net/api"
	const apiURL = "https://localhost:9443/api/v1"
	for name, value := range map[string]string{
		"NT_API_URL": apiURL, "NT_ACCOUNT_STORE": "file",
		"NT_ACCOUNT_CREDENTIALS_FILE": credentialPath, "SSL_CERT_FILE": "/certs/ca.pem",
	} {
		if os.Getenv(name) != value {
			t.Fatal("isolated local identity environment guard rejected")
		}
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		if os.Getenv(name) != "" {
			t.Fatal("isolated local identity proxy guard rejected")
		}
	}
	dockerMarker, markerErr := os.Lstat("/.dockerenv")
	if markerErr != nil || !dockerMarker.Mode().IsRegular() || os.Getuid() != 65532 || os.Geteuid() != 65532 {
		t.Fatal("isolated local identity container guard rejected")
	}
	var directory unix.Stat_t
	var filesystem unix.Statfs_t
	if unix.Lstat("/cli-account", &directory) != nil || directory.Mode&unix.S_IFMT != unix.S_IFDIR || directory.Mode&0777 != 0700 || directory.Uid != 65532 ||
		unix.Statfs("/cli-account", &filesystem) != nil || filesystem.Type != unix.TMPFS_MAGIC {
		t.Fatal("isolated local identity tmpfs guard rejected")
	}
	binary, binaryErr := os.Lstat("/nt")
	if binaryErr != nil || !binary.Mode().IsRegular() || binary.Mode().Perm()&0111 == 0 {
		t.Fatal("isolated local identity executable guard rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	api, err := runclient.New(runclient.Options{BaseURL: apiURL})
	if err != nil {
		t.Fatal("isolated local API initialization failed")
	}
	bootstrap, err := api.Bootstrap(ctx)
	if err != nil || bootstrap.OIDC.Issuer != issuer || bootstrap.OIDC.Resource != resource || bootstrap.OIDC.ClientID == "" {
		t.Fatal("isolated local identity bootstrap guard rejected")
	}
	store, err := NewFileStore(credentialPath)
	if err != nil {
		t.Fatal("isolated local credential store initialization failed")
	}
	previous, err := store.Load(ctx)
	if err != nil || previous.Issuer != issuer || previous.Resource != resource || previous.ClientID != bootstrap.OIDC.ClientID {
		t.Fatal("isolated local credential binding guard rejected")
	}
	options := Options{Issuer: issuer, Resource: resource, ClientID: bootstrap.OIDC.ClientID, Store: store}
	client, err := New(ctx, options)
	if err != nil {
		t.Fatal("isolated local authentication initialization failed")
	}
	accessToken, err := client.AccessToken(ctx)
	if err != nil {
		t.Fatal("real local refresh failed")
	}
	if _, err := api.Routes(ctx, accessToken); err != nil {
		t.Fatal("refreshed local access token could not list routes")
	}
	t.Log("real_refresh_and_routes_verified")
	latest, err := store.Load(ctx)
	if err != nil || !client.matches(latest) || !validCredentials(latest) || client.cached == nil ||
		latest.RefreshToken != client.cached.RefreshToken || latest.RefreshToken == previous.RefreshToken {
		t.Fatal("latest local refresh credential could not be retained")
	}
	t.Log("rotated_refresh_token_persisted_and_bound_verified")
	captured := &memoryStore{credentials: latest, present: true}
	defer func() { _ = captured.Delete(context.Background()) }()
	runCLI := func(command string) ([]byte, error) {
		commandCtx, commandCancel := context.WithTimeout(ctx, 30*time.Second)
		defer commandCancel()
		process := exec.CommandContext(commandCtx, "/nt", "--lang", "en", command)
		process.Env = []string{
			"NT_API_URL=" + apiURL, "NT_ACCOUNT_STORE=file", "NT_ACCOUNT_CREDENTIALS_FILE=" + credentialPath,
			"SSL_CERT_FILE=/certs/ca.pem", "LANG=C", "LC_ALL=C", "NO_COLOR=1",
		}
		process.WaitDelay = time.Second
		return process.CombinedOutput()
	}
	if _, err := runCLI("logout"); err != nil {
		t.Fatal("real local CLI logout did not succeed")
	}
	if _, err := os.Lstat(credentialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("real local CLI logout did not remove credentials")
	}
	t.Log("real_logout_and_local_credential_removal_verified")
	options.Store = captured
	revokedClient, err := New(ctx, options)
	if err != nil {
		t.Fatal("isolated revocation verifier initialization failed")
	}
	if _, err := revokedClient.AccessToken(ctx); !errors.Is(err, ErrAuthorizationRevoked) {
		t.Fatal("latest local refresh token was not confirmed revoked")
	}
	t.Log("latest_refresh_token_invalid_grant_verified")
	if client.cached == nil || !client.cached.Expiry.After(time.Now()) {
		t.Fatal("pre-logout access token expired before its acceptance check")
	}
	if _, err := api.Routes(ctx, accessToken); err != nil {
		t.Fatal("unexpired pre-logout access token was unexpectedly rejected")
	}
	t.Log("unexpired_pre_logout_access_token_remains_valid_verified")
	output, err := runCLI("routes")
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || !bytes.Contains(output, []byte("Log in first with nt login.")) {
		t.Fatal("new CLI process did not require login after logout")
	}
	t.Log("new_cli_process_requires_login_verified")
}
