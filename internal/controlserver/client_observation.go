package controlserver

import (
	"context"
	"errors"
	"net/http"
	"net/netip"

	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
)

var errClientUnconfirmed = errors.New("native client observation is unconfirmed")

type clientObserver struct {
	client *frpevidence.Client
}

func newClientObserver(endpoint, username, password string, supplied *http.Client) (*clientObserver, error) {
	client, err := frpevidence.NewClient(frpevidence.Options{Endpoint: endpoint, Username: username, Password: password, HTTPClient: supplied})
	if err != nil {
		return nil, err
	}
	return &clientObserver{client: client}, nil
}

func (c *clientObserver) connectedIP(ctx context.Context, runID string) (netip.Addr, error) {
	if c == nil || c.client == nil {
		return netip.Addr{}, errClientUnconfirmed
	}
	evidence := c.client.ObserveClient(ctx, runID)
	if evidence.Availability != frpevidence.Available || evidence.ClientID != runID || !evidence.Online || !evidence.ClientIP.IsValid() {
		return netip.Addr{}, errClientUnconfirmed
	}
	return evidence.ClientIP, nil
}
