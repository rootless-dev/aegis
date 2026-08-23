package configs_test

import (
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
)

// production returns a configuration that only lacks what each case is about,
// so a failure points at the rule under test rather than at the scaffolding.
func production(t *testing.T) *configs.Application {
	t.Helper()

	cfg := configs.Default()
	cfg.PublicURL = "https://auth.example.com"
	cfg.Database = serverDatabase()

	return cfg
}

func TestTerminationMatrix(t *testing.T) {
	cases := map[string]struct {
		arrange func(cfg *configs.Application)
		wants   string
	}{
		"undeclared is refused": {
			arrange: func(*configs.Application) {},
			wants:   "termination is required",
		},
		"unknown value is refused": {
			arrange: func(cfg *configs.Application) { cfg.TLS.Termination = "edge" },
			wants:   `unsupported termination "edge"`,
		},
		"app without a key pair is refused": {
			arrange: func(cfg *configs.Application) { cfg.TLS.Termination = configs.TerminationApp },
			wants:   "requires cert_file and key_file",
		},
		"app with half a key pair is refused": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationApp
				cfg.TLS.CertFile = "/etc/aegis/tls.crt"
			},
			wants: "have to be set together",
		},
		"app with a key pair is accepted": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationApp
				cfg.TLS.CertFile = "/etc/aegis/tls.crt"
				cfg.TLS.KeyFile = "/etc/aegis/tls.key"
			},
		},
		"proxy without trusted proxies is refused": {
			arrange: func(cfg *configs.Application) { cfg.TLS.Termination = configs.TerminationProxy },
			wants:   "trusted_proxies is required when a gateway terminates TLS",
		},
		"proxy carrying a certificate is refused": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationProxy
				cfg.Proxy.TrustedProxies = []string{"10.0.0.0/8"}
				cfg.TLS.CertFile = "/etc/aegis/tls.crt"
				cfg.TLS.KeyFile = "/etc/aegis/tls.key"
			},
			wants: "does not serve TLS",
		},
		"proxy with a trusted range is accepted": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationProxy
				cfg.Proxy.TrustedProxies = []string{"10.0.0.0/8"}
			},
		},
		"an unsupported header family is refused": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationProxy
				cfg.Proxy.TrustedProxies = []string{"10.0.0.0/8"}
				cfg.Proxy.Headers = "x-real-ip"
			},
			wants: `unsupported headers "x-real-ip"`,
		},
		"rfc 7239 is accepted": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationProxy
				cfg.Proxy.TrustedProxies = []string{"10.0.0.0/8"}
				cfg.Proxy.Headers = configs.HeadersForwarded
			},
		},
		"none with trusted proxies is refused": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationNone
				cfg.Proxy.TrustedProxies = []string{"10.0.0.0/8"}
			},
			wants: "only applies when a gateway terminates TLS",
		},
		"none is accepted": {
			arrange: func(cfg *configs.Application) { cfg.TLS.Termination = configs.TerminationNone },
		},
		"an unparseable trusted range is refused": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationProxy
				cfg.Proxy.TrustedProxies = []string{"10.0.0.0/8", "not-an-address"}
			},
			wants: "neither a CIDR block nor an address",
		},
		"a reload interval of zero is refused": {
			arrange: func(cfg *configs.Application) {
				cfg.TLS.Termination = configs.TerminationNone
				cfg.TLS.ReloadInterval = 0
			},
			wants: "reload interval must be greater than zero",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := production(t)
			tc.arrange(cfg)

			requireValidation(t, cfg.Validate(), tc.wants)
		})
	}
}

// requireValidation asserts the outcome of a validation: an empty want means
// the configuration has to be accepted, anything else has to be reported and
// named. It lives here for both files of this package, which otherwise repeat
// the same four branches around every table.
func requireValidation(t *testing.T, err error, wants string) {
	t.Helper()

	if wants == "" {
		if err != nil {
			t.Fatalf("want a valid configuration, got %v", err)
		}

		return
	}

	if err == nil {
		t.Fatalf("want an error mentioning %q, got none", wants)
	}

	if !strings.Contains(err.Error(), wants) {
		t.Errorf("want an error mentioning %q, got %v", wants, err)
	}
}

// Development is the one profile that may serve TLS with nothing configured,
// because it mints the certificate itself.
func TestDevelopmentNeedsNoKeyPair(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.Normalize()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("development should be valid with nothing declared, got %v", err)
	}

	if !cfg.TLS.GeneratesCertificate(cfg.Profile) {
		t.Error("development without a key pair should generate one")
	}

	if !cfg.TLS.ServesTLS() {
		t.Error("development should terminate TLS in the process")
	}
}

// A generated certificate is a development affordance, never something a
// production boot can fall back into.
func TestProductionNeverGeneratesACertificate(t *testing.T) {
	cfg := production(t)
	cfg.TLS.Termination = configs.TerminationApp

	if cfg.TLS.GeneratesCertificate(cfg.Profile) {
		t.Error("production must not mint its own certificate")
	}
}
