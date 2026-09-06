package frpevidence

import (
	"context"
	"encoding/json"
	"net/netip"
	"net/url"

	"github.com/Wy2926/nodelane-tunneld/internal/frpplugin"
)

// ClientEvidence observes the logical client's current native control. An
// offline record is useful only when the run is known to use fresh sessions.
type ClientEvidence struct {
	Availability    Availability
	ClientID        string
	NativeSessionID string
	Online          bool
	ClientIP        netip.Addr
	DisconnectedAt  int64
}

type nativeClient struct {
	Key              *string `json:"key"`
	User             *string `json:"user"`
	ClientID         *string `json:"clientID"`
	RunID            *string `json:"runID"`
	ClientIP         *string `json:"clientIP"`
	FirstConnectedAt *int64  `json:"firstConnectedAt"`
	LastConnectedAt  *int64  `json:"lastConnectedAt"`
	DisconnectedAt   *int64  `json:"disconnectedAt"`
	Online           *bool   `json:"online"`
}

type nativeClientPage struct {
	Total    *int           `json:"total"`
	Page     *int           `json:"page"`
	PageSize *int           `json:"pageSize"`
	Items    []nativeClient `json:"items"`
}

var clientListJSONShape = &jsonShape{fields: map[string]*jsonShape{
	"code": nil,
	"data": {fields: map[string]*jsonShape{
		"total": nil, "page": nil, "pageSize": nil,
		"items": {item: &jsonShape{fields: map[string]*jsonShape{
			"key": nil, "user": nil, "clientID": nil, "runID": nil, "clientIP": nil,
			"firstConnectedAt": nil, "lastConnectedAt": nil, "disconnectedAt": nil, "online": nil,
		}}},
	}},
}}

func (c *Client) ObserveClient(ctx context.Context, logicalRunID string) ClientEvidence {
	failed := ClientEvidence{Availability: Unavailable}
	if !validIdentifier(logicalRunID, "run_") && !validIdentifier(logicalRunID, "anr_") {
		return failed
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	// Stock frps clears runID when the explicit clientID goes offline. Do not
	// filter by native session or online status or the offline row disappears.
	query := url.Values{"user": {""}, "clientID": {logicalRunID}, "page": {"1"}, "pageSize": {"2"}}
	envelope, availability := c.get(ctx, "/api/v2/clients", query.Encode(), detailMaxBytes, clientListJSONShape)
	if availability != Available {
		return ClientEvidence{Availability: availability}
	}
	var page nativeClientPage
	if json.Unmarshal(envelope.Data, &page) != nil || page.Total == nil || *page.Total < 0 || *page.Total > 1 ||
		page.Page == nil || *page.Page != 1 || page.PageSize == nil || *page.PageSize != 2 ||
		page.Items == nil || len(page.Items) != *page.Total {
		return failed
	}
	if *page.Total == 0 {
		return ClientEvidence{Availability: NotObserved}
	}
	item := page.Items[0]
	if item.Key == nil || *item.Key != logicalRunID || item.ClientID == nil || *item.ClientID != logicalRunID ||
		item.User == nil || *item.User != "" || item.RunID == nil || item.Online == nil ||
		item.FirstConnectedAt == nil || *item.FirstConnectedAt < 0 || item.LastConnectedAt == nil || *item.LastConnectedAt < 0 ||
		item.DisconnectedAt != nil && *item.DisconnectedAt < 0 {
		return failed
	}
	result := ClientEvidence{Availability: Available, ClientID: *item.ClientID, NativeSessionID: *item.RunID, Online: *item.Online}
	if item.DisconnectedAt != nil {
		result.DisconnectedAt = *item.DisconnectedAt
	}
	if result.Online {
		if !frpplugin.ValidSessionID(result.NativeSessionID) || result.DisconnectedAt != 0 || item.ClientIP == nil || *item.ClientIP == "" {
			return failed
		}
	} else if result.NativeSessionID != "" || result.DisconnectedAt <= 0 {
		return failed
	}
	if item.ClientIP != nil && *item.ClientIP != "" {
		ip, err := netip.ParseAddr(*item.ClientIP)
		if err != nil || ip.Zone() != "" {
			return failed
		}
		ip = ip.Unmap()
		if ip.IsUnspecified() || ip.IsMulticast() {
			return failed
		}
		result.ClientIP = ip
	}
	return result
}
