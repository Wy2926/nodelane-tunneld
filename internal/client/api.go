package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type API struct {
	BaseURL string
	HTTP    *http.Client
}

type Credentials struct {
	ClientID    string `json:"client_id"`
	ClientToken string `json:"client_token"`
}

type FRPConnection struct {
	ServerAddr    string `json:"server_addr"`
	ServerPort    int    `json:"server_port"`
	AuthToken     string `json:"auth_token"`
	TLSServerName string `json:"tls_server_name"`
}

type Tunnel struct {
	ID             string        `json:"id"`
	ClientID       string        `json:"client_id"`
	Protocol       string        `json:"protocol"`
	Status         string        `json:"status"`
	ProxyName      string        `json:"proxy_name"`
	Subdomain      string        `json:"subdomain"`
	RemotePort     int           `json:"remote_port"`
	PublicURL      string        `json:"public_url"`
	TunnelToken    string        `json:"tunnel_token"`
	ExpiresAt      time.Time     `json:"expires_at"`
	BandwidthLimit string        `json:"bandwidth_limit"`
	FRP            FRPConnection `json:"frp"`
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("API request failed with status %d", e.Status)
}

func NewAPI(baseURL string) *API {
	return &API{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *API) Register(ctx context.Context) (Credentials, error) {
	var credentials Credentials
	err := a.do(ctx, http.MethodPost, "/clients", "", map[string]any{}, &credentials)
	return credentials, err
}

func (a *API) CreateTunnel(ctx context.Context, credentials Credentials, protocol string, localPort int, version string) (Tunnel, error) {
	var tunnel Tunnel
	err := a.do(ctx, http.MethodPost, "/tunnels", credentials.ClientToken, map[string]any{
		"protocol": protocol, "local_port": localPort, "client_version": version,
	}, &tunnel)
	return tunnel, err
}

func (a *API) GetTunnel(ctx context.Context, credentials Credentials, id string) (Tunnel, error) {
	var tunnel Tunnel
	err := a.do(ctx, http.MethodGet, "/tunnels/"+id, credentials.ClientToken, nil, &tunnel)
	return tunnel, err
}

func (a *API) DeleteTunnel(ctx context.Context, credentials Credentials, id string) error {
	return a.do(ctx, http.MethodDelete, "/tunnels/"+id, credentials.ClientToken, nil, nil)
}

func (a *API) do(ctx context.Context, method, path, token string, body, target any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "nodelane-tunnel-cli/"+Version)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := a.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload)
		return &APIError{Status: response.StatusCode, Code: payload.Error.Code, Message: payload.Error.Message}
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}
