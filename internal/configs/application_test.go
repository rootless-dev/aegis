package configs_test

import (
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
)

func TestPublicURLValidation(t *testing.T) {
	cases := map[string]struct {
		url         string
		termination configs.Termination
		wants       string
	}{
		"absent": {
			url:         "",
			termination: configs.TerminationNone,
			wants:       "public url is required",
		},
		"unsupported scheme": {
			url:         "ftp://auth.example.com",
			termination: configs.TerminationNone,
			wants:       "scheme must be http or https",
		},
		"no host": {
			url:         "https://",
			termination: configs.TerminationNone,
			wants:       "has no host",
		},
		// The extra parts would be concatenated into every issuer and redirect
		// built from this url.
		"carries a path": {
			url:         "https://auth.example.com/auth",
			termination: configs.TerminationNone,
			wants:       "drop the path",
		},
		"carries a query": {
			url:         "https://auth.example.com?realm=master",
			termination: configs.TerminationNone,
			wants:       "no query, fragment or credentials",
		},
		// Serving HTTPS while announcing http would hand out an issuer no client
		// can reach, and the failure would only ever surface on their side.
		"plain http while terminating in the app": {
			url:         "http://auth.example.com",
			termination: configs.TerminationApp,
			wants:       "cannot be",
		},
		"plain http behind a gateway": {
			url:         "http://auth.example.com",
			termination: configs.TerminationProxy,
			wants:       "cannot be",
		},
		// With nobody terminating TLS, http is the honest answer.
		"plain http with nothing in front": {
			url:         "http://auth.example.com",
			termination: configs.TerminationNone,
		},
		"a trailing slash is a base url": {
			url:         "https://auth.example.com/",
			termination: configs.TerminationNone,
		},
		"a port is kept": {
			url:         "https://auth.example.com:8443",
			termination: configs.TerminationNone,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			requireValidation(t, publicURLConfig(tc.url, tc.termination).Validate(), tc.wants)
		})
	}
}

// publicURLConfig builds a configuration whose only interesting part is the
// public url and the topology it has to agree with. The rest is whatever makes
// that topology valid, so a failure points at the rule under test.
func publicURLConfig(url string, termination configs.Termination) *configs.Application {
	cfg := configs.Default()
	cfg.PublicURL = url
	cfg.TLS.Termination = termination
	cfg.Database = serverDatabase()

	switch termination {
	case configs.TerminationApp:
		cfg.TLS.CertFile = "/etc/aegis/tls.crt"
		cfg.TLS.KeyFile = "/etc/aegis/tls.key"
	case configs.TerminationProxy:
		cfg.Proxy.TrustedProxies = []string{"10.0.0.0/8"}
	case configs.TerminationNone:
	}

	return cfg
}

func TestNormalizeOnlyHelpsDevelopment(t *testing.T) {
	cfg := configs.Default()
	cfg.Normalize()

	// Production is left exactly as declared, which is what makes the missing
	// topology a boot failure instead of an inherited guess.
	if cfg.TLS.Termination != "" {
		t.Errorf("termination: want it left empty, got %q", cfg.TLS.Termination)
	}

	if cfg.PublicURL != "" {
		t.Errorf("public url: want it left empty, got %q", cfg.PublicURL)
	}
}

func TestNormalizeKeepsWhatWasDeclared(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.TLS.Termination = configs.TerminationNone
	cfg.PublicURL = "http://dev.local:9000"
	cfg.Normalize()

	if cfg.TLS.Termination != configs.TerminationNone {
		t.Errorf("termination: want none, got %q", cfg.TLS.Termination)
	}

	if cfg.PublicURL != "http://dev.local:9000" {
		t.Errorf("public url: want the declared one, got %q", cfg.PublicURL)
	}
}

func TestPublicSchemeComesFromThePublicURL(t *testing.T) {
	cfg := configs.Default()
	cfg.PublicURL = "http://auth.example.com"

	if got := cfg.PublicScheme(); got != "http" {
		t.Errorf("want http, got %q", got)
	}

	// An unusable value must not downgrade the scheme every cookie and redirect
	// is built from.
	cfg.PublicURL = "://broken"

	if got := cfg.PublicScheme(); got != "https" {
		t.Errorf("want https as the safe fallback, got %q", got)
	}
}

func TestHSTSHeaderValue(t *testing.T) {
	cfg := configs.Default()

	if got := cfg.HSTS.HeaderValue(); got != "max-age=31536000" {
		t.Errorf("want max-age=31536000, got %q", got)
	}

	cfg.HSTS.IncludeSubdomains = true

	if got := cfg.HSTS.HeaderValue(); got != "max-age=31536000; includeSubDomains" {
		t.Errorf("want the subdomain directive, got %q", got)
	}
}

func TestPublicHostDropsThePort(t *testing.T) {
	cfg := configs.Default()

	cases := map[string]string{
		"https://auth.example.com":      "auth.example.com",
		"https://auth.example.com:8443": "auth.example.com",
		"https://localhost:7500":        "localhost",
		"https://[2001:db8::1]:7500":    "2001:db8::1",
	}

	for url, want := range cases {
		cfg.PublicURL = url

		if got := cfg.PublicHost(); got != want {
			t.Errorf("%s: want %q, got %q", url, want, got)
		}
	}
}

// Case folding covers every enum the layers can spell differently, not just the
// two that had it first.
func TestNormalizeFoldsTheHeaderFamily(t *testing.T) {
	cfg := configs.Default()
	cfg.Proxy.Headers = "  FORWARDED  "
	cfg.Normalize()

	if cfg.Proxy.Headers != configs.HeadersForwarded {
		t.Errorf("want %q, got %q", configs.HeadersForwarded, cfg.Proxy.Headers)
	}
}
