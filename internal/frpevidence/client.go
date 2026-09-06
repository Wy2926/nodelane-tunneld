package frpevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout = 3 * time.Second
	detailMaxBytes = 64 << 10
	listMaxBytes   = 2 << 20
	listPageSize   = 200
	listMaxItems   = 10000
)

var ErrInvalidConfiguration = errors.New("invalid frps evidence configuration")

type Availability string

const (
	Available   Availability = "available"
	NotObserved Availability = "not_observed"
	Unavailable Availability = "unavailable"
)

type Expected struct {
	ProxyName string
	RunID     string
	Protocol  string
}

// Evidence is an identity-bound observation, not authority to release a run.
// In particular, NotObserved does not prove that frps released its resources.
type Evidence struct {
	Availability       Availability
	ProxyName          string
	RunID              string
	Protocol           string
	Phase              string
	CurrentConnections int64
}

// Inventory is a bounded, non-atomic observation of the stock frps API.
// Even Available must not alone clear a readiness fence or release resources.
type Inventory struct {
	Availability Availability
	Proxies      []Evidence
}

type Options struct {
	Endpoint   string
	Username   string
	Password   string
	HTTPClient *http.Client
}

// Client reads only fixed stock-frps v0.70 proxy endpoints. It has no prune,
// delete, or caller-supplied URL operation.
type Client struct {
	endpoint string
	username string
	password string
	http     *http.Client
}

func NewClient(options Options) (*Client, error) {
	endpoint, err := parseEndpoint(options.Endpoint)
	if err != nil || options.Username == "" || len(options.Username) > 1024 ||
		strings.ContainsAny(options.Username, ":\r\n") || options.Password == "" || len(options.Password) > 4096 {
		return nil, ErrInvalidConfiguration
	}
	client := http.Client{}
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > requestTimeout {
		client.Timeout = requestTimeout
	}
	return &Client{
		endpoint: strings.TrimSuffix(endpoint.String(), "/"),
		username: options.Username,
		password: options.Password,
		http:     &client,
	}, nil
}

func parseEndpoint(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsRune(raw, '#') {
		return nil, ErrInvalidConfiguration
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Hostname() == "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.RawPath != "" && parsed.RawPath != "/") {
		return nil, ErrInvalidConfiguration
	}
	switch parsed.Scheme {
	case "https":
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
	} else if strings.HasSuffix(parsed.Host, ":") {
		return nil, ErrInvalidConfiguration
	}
	return parsed, nil
}

type nativeEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

type nativeProxy struct {
	Name     string  `json:"name"`
	ClientID *string `json:"clientID"`
	Spec     struct {
		Type string `json:"type"`
	} `json:"spec"`
	Status struct {
		Phase    string `json:"phase"`
		CurConns *int64 `json:"curConns"`
	} `json:"status"`
}

type nativePage struct {
	Total    *int          `json:"total"`
	Page     *int          `json:"page"`
	PageSize *int          `json:"pageSize"`
	Items    []nativeProxy `json:"items"`
}

func (c *Client) Observe(ctx context.Context, expected Expected) Evidence {
	result := Evidence{Availability: Unavailable}
	if !validExpected(expected) {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	envelope, availability := c.get(ctx, "/api/v2/proxies/"+expected.ProxyName, "", detailMaxBytes)
	if availability != Available {
		return Evidence{Availability: availability}
	}
	var proxy nativeProxy
	if json.Unmarshal(envelope.Data, &proxy) != nil || !proxy.valid(expected.Protocol) ||
		proxy.Name != expected.ProxyName || *proxy.ClientID != expected.RunID {
		return result
	}
	return proxy.evidence()
}

// ListAnonymous enumerates every supported protocol, including offline rows.
// Any incomplete page or inconsistent row invalidates the whole inventory.
func (c *Client) ListAnonymous(ctx context.Context) Inventory {
	failed := Inventory{Availability: Unavailable}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	byName := make(map[string]Evidence)
	seenNames := make(map[string]bool)
	totalItems := 0
	for _, protocol := range []string{"http", "tcp", "udp"} {
		previousTotal := -1
		for page := 1; ; page++ {
			query := url.Values{"type": {protocol}, "page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(listPageSize)}}
			envelope, availability := c.get(ctx, "/api/v2/proxies", query.Encode(), listMaxBytes)
			if availability != Available {
				return failed
			}
			var data nativePage
			if json.Unmarshal(envelope.Data, &data) != nil || data.Total == nil || data.Page == nil || data.PageSize == nil || data.Items == nil ||
				*data.Total < 0 || *data.Total > listMaxItems || *data.Page != page || *data.PageSize != listPageSize ||
				(previousTotal >= 0 && *data.Total != previousTotal) {
				return failed
			}
			if page == 1 {
				totalItems += *data.Total
				if totalItems > listMaxItems {
					return failed
				}
			}
			previousTotal = *data.Total
			remaining := *data.Total - (page-1)*listPageSize
			if remaining < 0 || len(data.Items) != min(listPageSize, remaining) {
				return failed
			}
			for _, proxy := range data.Items {
				if !proxy.valid(protocol) || seenNames[proxy.Name] {
					return failed
				}
				seenNames[proxy.Name] = true
				if !strings.HasPrefix(proxy.Name, "anon_") {
					if strings.HasPrefix(*proxy.ClientID, "anr_") {
						return failed
					}
					continue
				}
				if !validExpected(Expected{ProxyName: proxy.Name, RunID: *proxy.ClientID, Protocol: protocol}) {
					return failed
				}
				byName[proxy.Name] = proxy.evidence()
			}
			if remaining <= listPageSize {
				break
			}
		}
	}
	result := Inventory{Availability: Available, Proxies: make([]Evidence, 0, len(byName))}
	for _, evidence := range byName {
		result.Proxies = append(result.Proxies, evidence)
	}
	sort.Slice(result.Proxies, func(i, j int) bool { return result.Proxies[i].ProxyName < result.Proxies[j].ProxyName })
	return result
}

func (c *Client) get(ctx context.Context, path, query string, maxBytes int64) (nativeEnvelope, Availability) {
	var envelope nativeEnvelope
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return envelope, Unavailable
	}
	request.URL.RawQuery = query
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(c.username, c.password)
	response, err := c.http.Do(request)
	if err != nil {
		return envelope, Unavailable
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound) {
		return envelope, Unavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || int64(len(body)) > maxBytes || !unambiguousJSON(body) || json.Unmarshal(body, &envelope) != nil || envelope.Code != response.StatusCode {
		return envelope, Unavailable
	}
	if response.StatusCode == http.StatusNotFound {
		if string(envelope.Data) != "null" {
			return envelope, Unavailable
		}
		return envelope, NotObserved
	}
	return envelope, Available
}

func unambiguousJSON(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if !unambiguousValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func unambiguousValue(decoder *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return true
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return false
			}
			// encoding/json also matches struct field names case-insensitively.
			key = strings.ToLower(key)
			if seen[key] || !unambiguousValue(decoder, depth+1) {
				return false
			}
			seen[key] = true
		}
	case '[':
		for decoder.More() {
			if !unambiguousValue(decoder, depth+1) {
				return false
			}
		}
	default:
		return false
	}
	closing, err := decoder.Token()
	return err == nil && ((delimiter == '{' && closing == json.Delim('}')) || (delimiter == '[' && closing == json.Delim(']')))
}

func (p nativeProxy) valid(protocol string) bool {
	return p.Name != "" && p.ClientID != nil && p.Spec.Type == protocol &&
		(p.Status.Phase == "online" || p.Status.Phase == "offline") && p.Status.CurConns != nil && *p.Status.CurConns >= 0
}

func (p nativeProxy) evidence() Evidence {
	return Evidence{
		Availability: Available, ProxyName: p.Name, RunID: *p.ClientID, Protocol: p.Spec.Type,
		Phase: p.Status.Phase, CurrentConnections: *p.Status.CurConns,
	}
}

func validExpected(expected Expected) bool {
	if expected.Protocol != "http" && expected.Protocol != "tcp" && expected.Protocol != "udp" {
		return false
	}
	return (validIdentifier(expected.ProxyName, "rte_") && validIdentifier(expected.RunID, "run_")) ||
		(validIdentifier(expected.ProxyName, "anon_") && validIdentifier(expected.RunID, "anr_"))
}

func validIdentifier(value, prefix string) bool {
	if len(value) != len(prefix)+26 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < 'a' || character > 'z') && (character < '2' || character > '7') {
			return false
		}
	}
	return true
}
