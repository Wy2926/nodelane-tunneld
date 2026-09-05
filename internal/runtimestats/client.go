package runtimestats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 64 << 10
	defaultTimeout   = 3 * time.Second
)

var ErrInvalidConfiguration = errors.New("invalid runtime statistics configuration")

type Availability string

const (
	Available   Availability = "available"
	NotObserved Availability = "not_observed"
	Unavailable Availability = "unavailable"
)

// Snapshot is the complete public boundary for frps runtime statistics. It
// intentionally excludes proxy configuration, user and client identifiers.
type Snapshot struct {
	CurrentConnections *int64       `json:"current_connections"`
	UploadBytesToday   *int64       `json:"upload_bytes_today"`
	DownloadBytesToday *int64       `json:"download_bytes_today"`
	ProxyState         *string      `json:"proxy_state"`
	ObservedAt         time.Time    `json:"observed_at"`
	Availability       Availability `json:"availability"`
}

type Options struct {
	Endpoint   string
	Username   string
	Password   string
	HTTPClient *http.Client
	Now        func() time.Time
}

// Client reads only the fixed frps v2 proxy-detail endpoint. It never accepts
// a URL or API path from a request and never follows redirects with credentials.
type Client struct {
	endpoint string
	username string
	password string
	http     *http.Client
	now      func() time.Time
}

func NewClient(options Options) (*Client, error) {
	endpoint, err := parseEndpoint(options.Endpoint)
	if err != nil || !validBasicUsername(options.Username) || options.Password == "" || len(options.Password) > 4096 {
		return nil, ErrInvalidConfiguration
	}

	client := http.Client{}
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if client.Timeout <= 0 || client.Timeout > 5*time.Second {
		client.Timeout = defaultTimeout
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		endpoint: strings.TrimSuffix(endpoint.String(), "/"),
		username: options.Username,
		password: options.Password,
		http:     &client,
		now:      now,
	}, nil
}

func parseEndpoint(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, ErrInvalidConfiguration
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return nil, ErrInvalidConfiguration
	}
	if (parsed.Path != "" && parsed.Path != "/") || (parsed.RawPath != "" && parsed.RawPath != "/") {
		return nil, ErrInvalidConfiguration
	}
	switch parsed.Scheme {
	case "https":
		// HTTPS is suitable for an explicitly configured trusted management
		// network endpoint. Reachability is a deployment concern.
	case "http":
		address, err := netip.ParseAddr(parsed.Hostname())
		if err != nil || !address.IsLoopback() {
			return nil, ErrInvalidConfiguration
		}
	default:
		return nil, ErrInvalidConfiguration
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, ErrInvalidConfiguration
		}
	}
	return parsed, nil
}

func validBasicUsername(username string) bool {
	return username != "" && len(username) <= 1024 && !strings.ContainsAny(username, ":\r\n")
}

func validProxyName(name string) bool {
	if len(name) != len("rte_")+26 || !strings.HasPrefix(name, "rte_") {
		return false
	}
	for _, character := range name[len("rte_"):] {
		if character < 'a' || character > 'z' {
			if character < '2' || character > '7' {
				return false
			}
		}
	}
	return true
}

type nativeEnvelope struct {
	Code int          `json:"code"`
	Data *nativeProxy `json:"data"`
}

type nativeProxy struct {
	Name   string       `json:"name"`
	Status nativeStatus `json:"status"`
}

type nativeStatus struct {
	Phase           *string `json:"phase"`
	TodayTrafficIn  *int64  `json:"todayTrafficIn"`
	TodayTrafficOut *int64  `json:"todayTrafficOut"`
	CurrentConns    *int64  `json:"curConns"`
}

func (c *Client) Snapshot(ctx context.Context, proxyName string) Snapshot {
	observedAt := c.now().UTC()
	result := Snapshot{ObservedAt: observedAt, Availability: Unavailable}
	if !validProxyName(proxyName) {
		return result
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v2/proxies/"+proxyName, nil)
	if err != nil {
		return result
	}
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(c.username, c.password)
	response, err := c.http.Do(request)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		result.Availability = NotObserved
		return result
	}
	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get("Content-Type")) {
		return result
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return result
	}
	var envelope nativeEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.Code != http.StatusOK || envelope.Data == nil || envelope.Data.Name != proxyName {
		return result
	}
	status := envelope.Data.Status
	if status.Phase == nil || (*status.Phase != "online" && *status.Phase != "offline") ||
		status.TodayTrafficIn == nil || *status.TodayTrafficIn < 0 ||
		status.TodayTrafficOut == nil || *status.TodayTrafficOut < 0 ||
		status.CurrentConns == nil || *status.CurrentConns < 0 {
		return result
	}

	result.Availability = Available
	result.ProxyState = status.Phase
	result.DownloadBytesToday = status.TodayTrafficIn
	result.UploadBytesToday = status.TodayTrafficOut
	result.CurrentConnections = status.CurrentConns
	return result
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}
