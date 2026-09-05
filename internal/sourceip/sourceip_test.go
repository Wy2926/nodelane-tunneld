package sourceip

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestNewRejectsInvalidTrustedPrefix(t *testing.T) {
	_, err := New([]netip.Prefix{{}})
	if err == nil {
		t.Fatal("New() error = nil, want invalid trusted prefix error")
	}
}

func TestNewRejectsMappedPrefixThatCannotBeUnmappedExactly(t *testing.T) {
	_, err := New([]netip.Prefix{netip.MustParsePrefix("::ffff:0:0/95")})
	if err == nil {
		t.Fatal("New() error = nil, want ambiguous mapped prefix error")
	}
}

func TestRequestIPDoesNotTrustForwardingHeadersFromUntrustedPeer(t *testing.T) {
	extractor := mustNew(t, nil)
	request := newRequest("203.0.113.9:4000", "not-an-ip", "also-not-an-ip")

	got, err := extractor.RequestIP(request)
	if err != nil {
		t.Fatalf("RequestIP() error = %v", err)
	}
	if want := netip.MustParseAddr("203.0.113.9"); got != want {
		t.Fatalf("RequestIP() = %s, want %s", got, want)
	}
}

func TestRequestIPPrefersSingleRealIPFromTrustedPeer(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	request := newRequest("127.0.0.1:4000", "198.51.100.7", "not-an-ip")

	got, err := extractor.RequestIP(request)
	if err != nil {
		t.Fatalf("RequestIP() error = %v", err)
	}
	if want := netip.MustParseAddr("198.51.100.7"); got != want {
		t.Fatalf("RequestIP() = %s, want %s", got, want)
	}
}

func TestRequestIPRejectsMultipleRealIPValues(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	request := newRequest("127.0.0.1:4000", "", "")
	request.Header.Add("X-Real-IP", "198.51.100.7")
	request.Header.Add("X-Real-IP", "198.51.100.8")

	if _, err := extractor.RequestIP(request); err == nil {
		t.Fatal("RequestIP() error = nil, want multiple X-Real-IP values rejected")
	}
}

func TestRequestIPWalksForwardedChainRightToLeft(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	})
	request := newRequest("127.0.0.1:4000", "", "192.0.2.20, 198.51.100.8, 10.0.0.4")

	got, err := extractor.RequestIP(request)
	if err != nil {
		t.Fatalf("RequestIP() error = %v", err)
	}
	if want := netip.MustParseAddr("198.51.100.8"); got != want {
		t.Fatalf("RequestIP() = %s, want %s", got, want)
	}
}

func TestRequestIPWalksAllForwardedHeaderLinesAsOneChain(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	})
	request := newRequest("127.0.0.1:4000", "", "")
	request.Header.Add("X-Forwarded-For", "192.0.2.20")
	request.Header.Add("X-Forwarded-For", "198.51.100.8, 10.0.0.4")

	got, err := extractor.RequestIP(request)
	if err != nil {
		t.Fatalf("RequestIP() error = %v", err)
	}
	if want := netip.MustParseAddr("198.51.100.8"); got != want {
		t.Fatalf("RequestIP() = %s, want %s", got, want)
	}
}

func TestRequestIPReturnsRemoteWhenForwardedChainIsEmptyOrAllTrusted(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	})

	for _, forwarded := range []string{"", "10.0.0.4, 127.0.0.2"} {
		request := newRequest("127.0.0.1:4000", "", forwarded)
		got, err := extractor.RequestIP(request)
		if err != nil {
			t.Fatalf("RequestIP(X-Forwarded-For=%q) error = %v", forwarded, err)
		}
		if want := netip.MustParseAddr("127.0.0.1"); got != want {
			t.Fatalf("RequestIP(X-Forwarded-For=%q) = %s, want %s", forwarded, got, want)
		}
	}
}

func TestRequestIPUnmapsMappedIPv4AddressesAndTrustedPrefixes(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{netip.MustParsePrefix("::ffff:192.0.2.0/120")})
	request := newRequest("[::ffff:192.0.2.10]:4000", "::ffff:198.51.100.7", "")

	got, err := extractor.RequestIP(request)
	if err != nil {
		t.Fatalf("RequestIP() error = %v", err)
	}
	if want := netip.MustParseAddr("198.51.100.7"); got != want {
		t.Fatalf("RequestIP() = %s, want %s", got, want)
	}
}

func TestRequestIPRejectsMalformedTrustedInput(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
	})

	tests := []struct {
		name       string
		remoteAddr string
		realIP     string
		forwarded  string
	}{
		{name: "remote address", remoteAddr: "not-a-remote-address"},
		{name: "real ip", remoteAddr: "127.0.0.1:4000", realIP: "not-an-ip"},
		{name: "forwarded hop", remoteAddr: "127.0.0.1:4000", forwarded: "198.51.100.8, not-an-ip, 10.0.0.4"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := newRequest(test.remoteAddr, test.realIP, test.forwarded)
			if _, err := extractor.RequestIP(request); err == nil {
				t.Fatal("RequestIP() error = nil, want malformed address error")
			}
		})
	}
}

func TestRequestIPWithNoTrustedRangesNeverTrustsHeaders(t *testing.T) {
	extractor := mustNew(t, []netip.Prefix{})
	request := newRequest("127.0.0.1:4000", "198.51.100.7", "")

	got, err := extractor.RequestIP(request)
	if err != nil {
		t.Fatalf("RequestIP() error = %v", err)
	}
	if want := netip.MustParseAddr("127.0.0.1"); got != want {
		t.Fatalf("RequestIP() = %s, want %s", got, want)
	}
}

func mustNew(t *testing.T, trusted []netip.Prefix) *Extractor {
	t.Helper()
	extractor, err := New(trusted)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return extractor
}

func newRequest(remoteAddr, realIP, forwarded string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = remoteAddr
	if realIP != "" {
		request.Header.Set("X-Real-IP", realIP)
	}
	if forwarded != "" {
		request.Header.Set("X-Forwarded-For", forwarded)
	}
	return request
}
