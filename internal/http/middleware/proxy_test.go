package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/rootless-dev/aegis/internal/http/middleware"
)

func trusted(t *testing.T, blocks ...string) []netip.Prefix {
	t.Helper()

	networks := make([]netip.Prefix, 0, len(blocks))

	for _, block := range blocks {
		prefix, err := netip.ParsePrefix(block)
		if err != nil {
			t.Fatalf("parsing %q: %v", block, err)
		}

		networks = append(networks, prefix)
	}

	return networks
}

// resolve runs the middleware over a request and reports what the chain
// downstream would see.
func resolve(t *testing.T, opts middleware.ProxyOptions, r *http.Request) (scheme, clientIP string, forwardedKept bool) {
	t.Helper()

	handler := middleware.Proxy(opts)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		scheme = middleware.SchemeFrom(req.Context())
		clientIP = middleware.ClientIPFrom(req.Context())
		forwardedKept = req.Header.Get(middleware.ForwardedForHeader) != ""
	}))

	handler.ServeHTTP(httptest.NewRecorder(), r)

	return scheme, clientIP, forwardedKept
}

func requestFrom(peer string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer

	for name, value := range headers {
		r.Header.Set(name, value)
	}

	return r
}

func TestForwardedHeadersFromATrustedProxyAreHonored(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Scheme:                "https",
	}

	scheme, clientIP, _ := resolve(t, opts, requestFrom("10.0.0.7:44321", map[string]string{
		middleware.ForwardedProtoHeader: "https",
		middleware.ForwardedForHeader:   "203.0.113.5",
	}))

	if scheme != "https" {
		t.Errorf("scheme: want https, got %q", scheme)
	}

	if clientIP != "203.0.113.5" {
		t.Errorf("client ip: want 203.0.113.5, got %q", clientIP)
	}
}

// Everything to the left of the first untrusted hop was written by a party
// outside the trust boundary and could have been made up.
func TestTheChainIsWalkedFromTheRight(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Scheme:                "https",
	}

	_, clientIP, _ := resolve(t, opts, requestFrom("10.0.0.7:44321", map[string]string{
		middleware.ForwardedForHeader: "1.2.3.4, 203.0.113.5, 10.0.0.9",
	}))

	// 1.2.3.4 is what the client claimed; 203.0.113.5 is the furthest back a
	// trusted proxy actually vouched for.
	if clientIP != "203.0.113.5" {
		t.Errorf("client ip: want 203.0.113.5, got %q", clientIP)
	}
}

func TestAChainOfOnlyTrustedHopsYieldsTheOriginalClient(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Scheme:                "https",
	}

	_, clientIP, _ := resolve(t, opts, requestFrom("10.0.0.7:44321", map[string]string{
		middleware.ForwardedForHeader: "10.0.0.1, 10.0.0.2",
	}))

	if clientIP != "10.0.0.1" {
		t.Errorf("client ip: want 10.0.0.1, got %q", clientIP)
	}
}

// An untrusted peer setting the header is exactly the attack the trust boundary
// exists for: a forged https would make a cookie marked Secure look safe to send
// over a connection that is not.
func TestForwardedHeadersFromAnUntrustedPeerAreDropped(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Scheme:                "http",
	}

	scheme, clientIP, kept := resolve(t, opts, requestFrom("198.51.100.9:44321", map[string]string{
		middleware.ForwardedProtoHeader: "https",
		middleware.ForwardedForHeader:   "203.0.113.5",
	}))

	if scheme != "http" {
		t.Errorf("scheme: a forged header must not win, got %q", scheme)
	}

	if clientIP != "198.51.100.9" {
		t.Errorf("client ip: want the peer, got %q", clientIP)
	}

	// Removed rather than ignored, so nothing downstream can read what nobody
	// vouched for.
	if kept {
		t.Error("the forwarded headers should have been stripped from the request")
	}
}

// With no gateway declared, the headers carry no authority at all, whoever sent
// them.
func TestForwardedHeadersAreIgnoredWhenNoProxyWasDeclared(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: false,
		Scheme:                "https",
	}

	scheme, clientIP, kept := resolve(t, opts, requestFrom("10.0.0.7:44321", map[string]string{
		middleware.ForwardedProtoHeader: "http",
		middleware.ForwardedForHeader:   "203.0.113.5",
	}))

	if scheme != "https" {
		t.Errorf("scheme: want the declared one, got %q", scheme)
	}

	if clientIP != "10.0.0.7" {
		t.Errorf("client ip: want the peer, got %q", clientIP)
	}

	if kept {
		t.Error("the forwarded headers should have been stripped from the request")
	}
}

func TestUnusableForwardedValuesFallBackToThePeer(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Scheme:                "https",
	}

	cases := map[string]string{
		"not an address": "unknown",
		"empty entry":    ", ",
	}

	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			_, clientIP, _ := resolve(t, opts, requestFrom("10.0.0.7:44321", map[string]string{
				middleware.ForwardedForHeader: header,
			}))

			if clientIP != "10.0.0.7" {
				t.Errorf("client ip: want the peer, got %q", clientIP)
			}
		})
	}
}

func TestAnUnsupportedForwardedSchemeIsRefused(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Scheme:                "https",
	}

	scheme, _, _ := resolve(t, opts, requestFrom("10.0.0.7:44321", map[string]string{
		middleware.ForwardedProtoHeader: "gopher",
	}))

	if scheme != "https" {
		t.Errorf("scheme: want the declared one, got %q", scheme)
	}
}

// An IPv4 client arriving through an IPv6 aware proxy still has to match an
// IPv4 trusted range.
func TestMappedAddressesMatchTheirIPv4Range(t *testing.T) {
	opts := middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Scheme:                "https",
	}

	_, clientIP, _ := resolve(t, opts, requestFrom("[::ffff:10.0.0.7]:44321", map[string]string{
		middleware.ForwardedForHeader: "203.0.113.5",
	}))

	if clientIP != "203.0.113.5" {
		t.Errorf("client ip: want 203.0.113.5, got %q", clientIP)
	}
}

func TestHSTSOnlyAnswersOverHTTPS(t *testing.T) {
	const value = "max-age=31536000"

	cases := map[string]struct {
		scheme string
		wants  string
	}{
		"https": {scheme: "https", wants: value},
		// Over plain HTTP the header asks the browser to trust the one message
		// an attacker on the path could have written.
		"http": {scheme: "http", wants: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			chain := middleware.Proxy(middleware.ProxyOptions{Scheme: tc.scheme})(
				middleware.HSTS(value)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})),
			)

			recorder := httptest.NewRecorder()
			chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

			if got := recorder.Header().Get(middleware.StrictTransportSecurityHeader); got != tc.wants {
				t.Errorf("want %q, got %q", tc.wants, got)
			}
		})
	}
}
