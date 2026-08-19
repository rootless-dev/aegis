package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rootless-dev/aegis/internal/http/middleware"
)

func rfcOptions(t *testing.T) middleware.ProxyOptions {
	t.Helper()

	return middleware.ProxyOptions{
		TrustForwardedHeaders: true,
		TrustedProxies:        trusted(t, "10.0.0.0/8"),
		Headers:               middleware.HeadersForwarded,
		Scheme:                "http",
	}
}

func TestRFC7239IsReadWhenTheGatewaySpeaksIt(t *testing.T) {
	cases := map[string]struct {
		header string
		scheme string
		client string
	}{
		"single hop": {
			header: `for=203.0.113.5;proto=https;by=10.0.0.7`,
			scheme: "https",
			client: "203.0.113.5",
		},
		// Quoting is what an IPv6 address needs: the value carries both colons
		// and brackets, so splitting has to respect the quotes.
		"quoted ipv6 with port": {
			header: `for="[2001:db8::1]:4711";proto=https`,
			scheme: "https",
			client: "2001:db8::1",
		},
		// Same rule as the other family: everything left of the first untrusted
		// hop was written outside the trust boundary.
		"chain walked from the right": {
			header: `for=1.2.3.4, for=203.0.113.5, for=10.0.0.9`,
			scheme: "http",
			client: "203.0.113.5",
		},
		"parameters are case insensitive": {
			header: `For=203.0.113.5;Proto=HTTPS`,
			scheme: "https",
			client: "203.0.113.5",
		},
		"unknown parameters are ignored": {
			header: `by=10.0.0.7;for=203.0.113.5;host=auth.example.com;proto=https`,
			scheme: "https",
			client: "203.0.113.5",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scheme, clientIP, _ := resolve(t, rfcOptions(t), requestFrom("10.0.0.7:44321", map[string]string{
				middleware.ForwardedHeader: tc.header,
			}))

			if scheme != tc.scheme {
				t.Errorf("scheme: want %q, got %q", tc.scheme, scheme)
			}

			if clientIP != tc.client {
				t.Errorf("client ip: want %q, got %q", tc.client, clientIP)
			}
		})
	}
}

// A deployment has one gateway writing one family. Reading both would mean
// deciding what to do when they disagree.
func TestOnlyTheConfiguredFamilyIsRead(t *testing.T) {
	headers := map[string]string{
		middleware.ForwardedHeader:      `for=203.0.113.5;proto=https`,
		middleware.ForwardedForHeader:   "198.51.100.9",
		middleware.ForwardedProtoHeader: "http",
	}

	t.Run("rfc 7239 selected", func(t *testing.T) {
		scheme, clientIP, _ := resolve(t, rfcOptions(t), requestFrom("10.0.0.7:44321", headers))

		if scheme != "https" || clientIP != "203.0.113.5" {
			t.Errorf("want the Forwarded values, got scheme=%q client=%q", scheme, clientIP)
		}
	})

	t.Run("x-forwarded selected", func(t *testing.T) {
		opts := rfcOptions(t)
		opts.Headers = middleware.HeadersXForwarded

		scheme, clientIP, _ := resolve(t, opts, requestFrom("10.0.0.7:44321", headers))

		if scheme != "http" || clientIP != "198.51.100.9" {
			t.Errorf("want the X-Forwarded values, got scheme=%q client=%q", scheme, clientIP)
		}
	})
}

// The family that is not spoken here is removed either way, so nothing
// downstream can pick up what this deployment decided not to read.
func TestTheUnreadFamilyIsStripped(t *testing.T) {
	request := requestFrom("10.0.0.7:44321", map[string]string{
		middleware.ForwardedHeader:    `for=203.0.113.5;proto=https`,
		middleware.ForwardedForHeader: "198.51.100.9",
	})

	handler := middleware.Proxy(rfcOptions(t))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(middleware.ForwardedForHeader); got != "" {
			t.Errorf("X-Forwarded-For should have been stripped, got %q", got)
		}

		if got := r.Header.Get(middleware.ForwardedHeader); got == "" {
			t.Error("the configured family should reach the handler intact")
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), request)
}

func TestRFC7239FromAnUntrustedPeerIsDropped(t *testing.T) {
	scheme, clientIP, _ := resolve(t, rfcOptions(t), requestFrom("198.51.100.9:44321", map[string]string{
		middleware.ForwardedHeader: `for=203.0.113.5;proto=https`,
	}))

	if scheme != "http" {
		t.Errorf("scheme: a forged header must not win, got %q", scheme)
	}

	if clientIP != "198.51.100.9" {
		t.Errorf("client ip: want the peer, got %q", clientIP)
	}
}
