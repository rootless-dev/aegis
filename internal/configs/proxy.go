package configs

import (
	"errors"
	"fmt"
	"net/netip"
)

// Proxy is the trust boundary for the forwarded headers. Reading them from an
// undeclared peer is what turns X-Forwarded-Proto into a way for any client to
// claim it arrived over HTTPS.
// ForwardedHeaders names the family the gateway in front writes. Only one is
// read: a deployment has one gateway, and accepting both would mean deciding
// what to do when they disagree.
type ForwardedHeaders string

const (
	// HeadersXForwarded is the de facto standard every ingress writes.
	HeadersXForwarded ForwardedHeaders = "x-forwarded"

	// HeadersForwarded is RFC 7239, which some proxies emit instead.
	HeadersForwarded ForwardedHeaders = "forwarded"
)

type Proxy struct {
	// TrustedProxies takes CIDR blocks or bare addresses, which are read as a
	// single host.
	TrustedProxies []string `yaml:"trusted_proxies"`

	Headers ForwardedHeaders `yaml:"headers"`
}

func defaultProxy() *Proxy {
	return &Proxy{
		Headers: HeadersXForwarded,
	}
}

// Validate takes what the deployment does rather than the termination value, so
// the meaning of each topology stays in tls.go.
func (cfg *Proxy) Validate(behindGateway bool) error {
	var errs []error

	if behindGateway && len(cfg.TrustedProxies) == 0 {
		errs = append(errs, errors.New(
			"proxy: trusted_proxies is required when a gateway terminates TLS, otherwise any client could forge the forwarded headers",
		))
	}

	if !behindGateway && len(cfg.TrustedProxies) > 0 {
		errs = append(errs, errors.New(
			"proxy: trusted_proxies only applies when a gateway terminates TLS",
		))
	}

	if cfg.Headers != HeadersXForwarded && cfg.Headers != HeadersForwarded {
		errs = append(errs, fmt.Errorf(
			"proxy: unsupported headers %q, want %q or %q", cfg.Headers, HeadersXForwarded, HeadersForwarded,
		))
	}

	if _, err := cfg.Networks(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func (cfg *Proxy) Networks() ([]netip.Prefix, error) {
	networks := make([]netip.Prefix, 0, len(cfg.TrustedProxies))

	var errs []error

	for _, entry := range cfg.TrustedProxies {
		prefix, err := parseNetwork(entry)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		networks = append(networks, prefix)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return networks, nil
}

func parseNetwork(entry string) (netip.Prefix, error) {
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		// Masked, otherwise 10.1.2.3/8 would keep the host bits and match
		// nothing but itself.
		return prefix.Masked(), nil
	}

	address, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("proxy: %q is neither a CIDR block nor an address", entry)
	}

	return netip.PrefixFrom(address, address.BitLen()), nil
}
