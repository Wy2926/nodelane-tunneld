// Package sourceip resolves a request's client address across explicitly
// trusted reverse proxies.
package sourceip

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// Extractor resolves client addresses using only forwarding headers supplied by
// an explicitly trusted peer.
type Extractor struct {
	trusted []netip.Prefix
}

// New validates and copies the trusted proxy ranges. An empty list trusts no
// proxies.
func New(trusted []netip.Prefix) (*Extractor, error) {
	normalized := make([]netip.Prefix, 0, len(trusted))
	for i, prefix := range trusted {
		if !prefix.IsValid() {
			return nil, fmt.Errorf("trusted proxy prefix %d is invalid", i)
		}

		address := prefix.Addr()
		bits := prefix.Bits()
		if address.Is4In6() {
			if bits < 96 {
				return nil, fmt.Errorf("trusted proxy prefix %d cannot be represented after IPv4 unmapping", i)
			}
			prefix = netip.PrefixFrom(address.Unmap(), bits-96)
		}
		normalized = append(normalized, prefix.Masked())
	}
	return &Extractor{trusted: normalized}, nil
}

// RequestIP returns the direct peer unless that peer is trusted. For a trusted
// peer, one X-Real-IP value takes precedence; otherwise X-Forwarded-For is
// walked from right to left until the first untrusted hop.
func (e *Extractor) RequestIP(request *http.Request) (netip.Addr, error) {
	remote, err := parseAddress(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse remote address: %w", err)
	}
	remote = remote.Unmap()
	if !e.isTrusted(remote) {
		return remote, nil
	}

	realIPValues := request.Header.Values("X-Real-IP")
	if len(realIPValues) > 1 {
		return netip.Addr{}, fmt.Errorf("parse X-Real-IP: multiple values")
	}
	if len(realIPValues) == 1 {
		if raw := strings.TrimSpace(realIPValues[0]); raw != "" {
			clientIP, err := parseAddress(raw)
			if err != nil {
				return netip.Addr{}, fmt.Errorf("parse X-Real-IP: %w", err)
			}
			return clientIP.Unmap(), nil
		}
	}

	forwarded := strings.Split(strings.Join(request.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		raw := strings.TrimSpace(forwarded[i])
		if raw == "" {
			continue
		}
		candidate, err := parseAddress(raw)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("parse X-Forwarded-For: %w", err)
		}
		candidate = candidate.Unmap()
		if !e.isTrusted(candidate) {
			return candidate, nil
		}
	}
	return remote, nil
}

func (e *Extractor) isTrusted(address netip.Addr) bool {
	for _, prefix := range e.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func parseAddress(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if addressPort, err := netip.ParseAddrPort(value); err == nil {
		return addressPort.Addr(), nil
	}
	return netip.ParseAddr(strings.Trim(value, "[]"))
}
