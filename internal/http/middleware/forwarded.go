package middleware

import (
	"net/http"
	"strings"
)

const ForwardedHeader = "Forwarded"

// forwardedHop is one element of the Forwarded header. Only the two parameters
// this deployment acts on are kept: "by" and "host" describe hops and names
// nothing here decides anything from.
type forwardedHop struct {
	client string
	proto  string
}

// parseForwarded reads RFC 7239, whose shape is a comma separated list of
// elements, each a semicolon separated list of key=value pairs where the value
// may be quoted. Quoting is what an IPv6 address needs — for="[2001:db8::1]:80"
// carries both colons and brackets — so splitting has to respect it.
func parseForwarded(values []string) []forwardedHop {
	var hops []forwardedHop

	for _, value := range values {
		for _, element := range splitOutsideQuotes(value, ',') {
			hop := forwardedHop{}

			for _, pair := range splitOutsideQuotes(element, ';') {
				key, raw, found := strings.Cut(pair, "=")
				if !found {
					continue
				}

				switch strings.ToLower(strings.TrimSpace(key)) {
				case "for":
					hop.client = unquote(raw)
				case "proto":
					hop.proto = strings.ToLower(unquote(raw))
				}
			}

			hops = append(hops, hop)
		}
	}

	return hops
}

func forwardedSchemeRFC(r *http.Request) string {
	hops := parseForwarded(r.Header.Values(ForwardedHeader))
	if len(hops) == 0 {
		return ""
	}

	// The leftmost element is the client's; the ones after it describe hops the
	// client never made.
	switch hops[0].proto {
	case "http", "https":
		return hops[0].proto
	default:
		return ""
	}
}

// forwardedClientRFC walks the chain from the right exactly like the
// X-Forwarded-For reader does, and for the same reason: everything to the left
// of the first untrusted hop was written outside the trust boundary.
func forwardedClientRFC(r *http.Request, trusted trustedNetworks) (string, bool) {
	hops := parseForwarded(r.Header.Values(ForwardedHeader))
	if len(hops) == 0 {
		return "", false
	}

	chain := make([]string, 0, len(hops))
	for _, hop := range hops {
		chain = append(chain, hop.client)
	}

	return clientFromChain(chain, trusted)
}

func splitOutsideQuotes(value string, separator byte) []string {
	var (
		parts  []string
		start  int
		quoted bool
	)

	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '"':
			quoted = !quoted
		case value[i] == separator && !quoted:
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}

	return append(parts, value[start:])
}

func unquote(value string) string {
	trimmed := strings.TrimSpace(value)

	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		return trimmed[1 : len(trimmed)-1]
	}

	return trimmed
}
