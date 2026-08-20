package middleware

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

const (
	ForwardedProtoHeader = "X-Forwarded-Proto"
	ForwardedForHeader   = "X-Forwarded-For"
	ForwardedHostHeader  = "X-Forwarded-Host"
	ForwardedPortHeader  = "X-Forwarded-Port"
)

// maxForwardedHops bounds how far back a chain is walked. No real deployment
// stacks that many proxies, and without a bound a single crafted header would
// have every request parse an arbitrary list of addresses.
const maxForwardedHops = 64

// ForwardedHeaders names the family a gateway speaks. Only one is read: a
// deployment has a gateway, and that gateway writes one of the two. Accepting
// both would mean deciding what to do when they disagree, which is a question
// with no good answer.
type ForwardedHeaders int

const (
	// HeadersXForwarded is the de facto standard every ingress writes.
	HeadersXForwarded ForwardedHeaders = iota

	// HeadersForwarded is RFC 7239, which some proxies emit instead.
	HeadersForwarded
)

type trustedNetworks []netip.Prefix

type ProxyOptions struct {
	// TrustForwardedHeaders is what an operator declared by terminating TLS at a
	// gateway. Without it the headers are not merely ignored, they are removed:
	// leaving them on the request invites the next handler to read what nobody
	// vouched for.
	TrustForwardedHeaders bool

	// TrustedProxies are the peers allowed to speak for the client.
	TrustedProxies []netip.Prefix

	// Headers selects which family is read. The other one is stripped either
	// way, so nothing downstream can pick up the family this deployment does
	// not speak.
	Headers ForwardedHeaders

	// Scheme is how clients reach this deployment when no trusted peer says
	// otherwise.
	Scheme string
}

// Proxy resolves the two things every request carries that a reverse proxy
// rewrites: the scheme the client actually used and the address it came from.
// Everything downstream reads those from the context rather than from headers,
// so trust is decided once, here.
func Proxy(opts ProxyOptions) Middleware {
	trusted := trustedNetworks(opts.TrustedProxies)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme := opts.Scheme
			clientIP := r.RemoteAddr

			peer, peerOK := parseForwardedAddr(r.RemoteAddr)
			if peerOK {
				clientIP = peer.String()
			}

			vouched := opts.TrustForwardedHeaders && peerOK && trusted.contains(peer)

			if vouched {
				if forwarded := resolveScheme(r, opts.Headers); forwarded != "" {
					scheme = forwarded
				}

				if forwarded, ok := resolveClient(r, opts.Headers, trusted); ok {
					clientIP = forwarded
				}
			}

			// The family that was not read is always removed; the one that was
			// is removed too when nobody vouched for it.
			stripForwardedHeaders(r, vouched, opts.Headers)

			ctx := context.WithValue(r.Context(), schemeContextKey, scheme)
			ctx = context.WithValue(ctx, clientIPContextKey, clientIP)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SchemeFrom reports how the client reached this deployment, which is not
// necessarily how the request reached this process.
func SchemeFrom(ctx context.Context) string {
	scheme, _ := ctx.Value(schemeContextKey).(string)

	return scheme
}

// ClientIPFrom reports the address to attribute the request to: the client's
// where a trusted proxy vouched for it, the peer's otherwise.
func ClientIPFrom(ctx context.Context) string {
	address, _ := ctx.Value(clientIPContextKey).(string)

	return address
}

func resolveScheme(r *http.Request, headers ForwardedHeaders) string {
	if headers == HeadersForwarded {
		return forwardedSchemeRFC(r)
	}

	// Only the first entry is the client's: the ones after it were added by
	// proxies further along, describing hops the client never made.
	value, _, _ := strings.Cut(r.Header.Get(ForwardedProtoHeader), ",")

	switch scheme := strings.ToLower(strings.TrimSpace(value)); scheme {
	case "http", "https":
		return scheme
	default:
		return ""
	}
}

func resolveClient(r *http.Request, headers ForwardedHeaders, trusted trustedNetworks) (string, bool) {
	if headers == HeadersForwarded {
		return forwardedClientRFC(r, trusted)
	}

	var chain []string

	for _, header := range r.Header.Values(ForwardedForHeader) {
		for _, entry := range strings.Split(header, ",") {
			chain = append(chain, strings.TrimSpace(entry))
		}
	}

	return clientFromChain(chain, trusted)
}

// clientFromChain walks the hops from the right, dropping the proxies that were
// declared trustworthy. The first address that is not one of them is as far back
// as this deployment can vouch for; anything to its left was written by a party
// outside our trust boundary and could have been made up.
func clientFromChain(chain []string, trusted trustedNetworks) (string, bool) {
	if len(chain) == 0 {
		return "", false
	}

	hops := 0

	for i := len(chain) - 1; i >= 0; i-- {
		hops++
		if hops > maxForwardedHops {
			return "", false
		}

		address, ok := parseForwardedAddr(chain[i])
		if !ok {
			return "", false
		}

		if !trusted.contains(address) {
			return address.String(), true
		}
	}

	// Every hop was ours, so the leftmost entry is the client.
	address, ok := parseForwardedAddr(chain[0])
	if !ok {
		return "", false
	}

	return address.String(), true
}

func stripForwardedHeaders(r *http.Request, trustedRequest bool, headers ForwardedHeaders) {
	xForwarded := []string{ForwardedProtoHeader, ForwardedForHeader, ForwardedHostHeader, ForwardedPortHeader}

	remove := func(names ...string) {
		for _, name := range names {
			r.Header.Del(name)
		}
	}

	if !trustedRequest {
		remove(append(xForwarded, ForwardedHeader)...)

		return
	}

	if headers == HeadersForwarded {
		remove(xForwarded...)

		return
	}

	remove(ForwardedHeader)
}

func parseForwardedAddr(value string) (netip.Addr, bool) {
	if address, err := netip.ParseAddr(value); err == nil {
		// Unmapped so an IPv4 address arriving in IPv6 form still matches an
		// IPv4 trusted range.
		return address.Unmap(), true
	}

	if addressPort, err := netip.ParseAddrPort(value); err == nil {
		return addressPort.Addr().Unmap(), true
	}

	return netip.Addr{}, false
}

func (t trustedNetworks) contains(address netip.Addr) bool {
	for _, network := range t {
		if network.Contains(address) {
			return true
		}
	}

	return false
}
