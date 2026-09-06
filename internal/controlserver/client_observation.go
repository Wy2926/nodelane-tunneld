package controlserver

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
	"strings"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	frpmodel "github.com/fatedier/frp/server/http/model"
)

var errClientUnconfirmed = errors.New("native client observation is unconfirmed")

type clientObserver struct {
	endpoint, username, password string
	http                         *http.Client
}

func newClientObserver(endpoint, username, password string, supplied *http.Client) (*clientObserver, error) {
	if _, err := frpevidence.NewClient(frpevidence.Options{Endpoint: endpoint, Username: username, Password: password}); err != nil {
		return nil, err
	}
	client := http.Client{}
	if supplied != nil {
		client = *supplied
	}
	client.Jar = nil
	client.Timeout = 3 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clientObserver{endpoint: strings.TrimSuffix(endpoint, "/"), username: username, password: password, http: &client}, nil
}

func (c *clientObserver) connectedIP(ctx context.Context, runID string) (netip.Addr, error) {
	if !validRunID(runID) {
		return netip.Addr{}, errClientUnconfirmed
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	query := url.Values{"user": {""}, "runID": {runID}, "clientID": {runID}, "status": {"online"}, "page": {"1"}, "pageSize": {"2"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v2/clients?"+query.Encode(), nil)
	if err != nil {
		return netip.Addr{}, errClientUnconfirmed
	}
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(c.username, c.password)
	response, err := c.http.Do(request)
	if err != nil {
		return netip.Addr{}, errClientUnconfirmed
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" {
		return netip.Addr{}, errClientUnconfirmed
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || validateNativeClientJSON(data) != nil {
		return netip.Addr{}, errClientUnconfirmed
	}
	var envelope struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			Total    *int                      `json:"total"`
			Page     *int                      `json:"page"`
			PageSize *int                      `json:"pageSize"`
			Items    []frpmodel.ClientInfoResp `json:"items"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || envelope.Code == nil || *envelope.Code != http.StatusOK || envelope.Data == nil {
		return netip.Addr{}, errClientUnconfirmed
	}
	page := envelope.Data
	if page.Total == nil || *page.Total != 1 || page.Page == nil || *page.Page != 1 || page.PageSize == nil || *page.PageSize != 2 || len(page.Items) != 1 {
		return netip.Addr{}, errClientUnconfirmed
	}
	item := page.Items[0]
	if item.ClientID != runID || item.RunID != runID || item.User != "" || !item.Online {
		return netip.Addr{}, errClientUnconfirmed
	}
	ip, err := netip.ParseAddr(item.ClientIP)
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return netip.Addr{}, errClientUnconfirmed
	}
	return ip.Unmap(), nil
}

func validRunID(id string) bool {
	if len(id) != 30 || !strings.HasPrefix(id, "run_") {
		return false
	}
	for _, character := range id[4:] {
		if !(character >= 'a' && character <= 'z' || character >= '2' && character <= '7') {
			return false
		}
	}
	return true
}

func validateNativeClientJSON(data []byte) error {
	allowed := map[string]bool{
		"code": true, "msg": true, "data": true, "total": true, "page": true, "pageSize": true, "items": true,
		"key": true, "user": true, "clientID": true, "runID": true, "version": true, "wireProtocol": true,
		"hostname": true, "clientIP": true, "firstConnectedAt": true, "lastConnectedAt": true, "disconnectedAt": true, "online": true,
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var consume func() error
	consume = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || !allowed[key] || seen[key] {
					return errClientUnconfirmed
				}
				seen[key] = true
				if err := consume(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case json.Delim('['):
			for decoder.More() {
				if err := consume(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return nil
		}
	}
	if err := consume(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errClientUnconfirmed
	}
	return nil
}
