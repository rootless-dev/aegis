package configs

import (
	"errors"
	"fmt"
	"time"
)

// Termination declares who ends the TLS connection. It has no default in
// production on purpose: "no certificate was configured" and "a gateway handles
// TLS" produce byte for byte the same configuration, and the only thing that
// tells them apart is an operator having said so.
type Termination string

const (
	// TerminationApp serves TLS from this process.
	TerminationApp Termination = "app"

	// TerminationProxy leaves TLS to a gateway that forwards the original scheme
	// and client address in headers.
	TerminationProxy Termination = "proxy"

	// TerminationNone is plain HTTP with nothing in front, or a service mesh
	// doing transparent mTLS. Forwarded headers are ignored under it: with no
	// declared proxy, nobody is entitled to set them.
	TerminationNone Termination = "none"
)

type TLS struct {
	Termination Termination `yaml:"termination"`

	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`

	// ReloadInterval re-reads the pair from disk. Issuers rotate the files in
	// place, and a certificate read once at boot would only be renewed by
	// restarting the process.
	ReloadInterval time.Duration `yaml:"reload_interval"`
}

func defaultTLS() *TLS {
	return &TLS{
		// Deliberately empty: only the operator knows whether a gateway sits in
		// front, and outside the dev profile the boot fails until they say.
		Termination:    "",
		ReloadInterval: time.Hour,
	}
}

func (t Termination) String() string {
	return string(t)
}

// The predicates below are the only place the termination values are read.
// Everywhere else asks what the deployment does, not which word was written in
// the configuration: adding a fourth topology should mean editing this file and
// nothing else.

// ServesTLS reports whether this process is the one performing the handshake.
func (cfg *TLS) ServesTLS() bool {
	return cfg.Termination == TerminationApp
}

// TrustsForwardedHeaders reports whether a declared gateway is allowed to speak
// for the client through X-Forwarded-*.
func (cfg *TLS) TrustsForwardedHeaders() bool {
	return cfg.Termination == TerminationProxy
}

// ServesClientsOverHTTPS reports whether clients reach this deployment over
// TLS, whoever terminates it. It is what the public url has to agree with.
func (cfg *TLS) ServesClientsOverHTTPS() bool {
	return cfg.ServesTLS() || cfg.TrustsForwardedHeaders()
}

// normalizeForDevelopment fills in the topology development does not have to
// declare: plain HTTP, with nothing in front. A self-signed certificate is an
// interstitial on every browser session, so HTTPS is opt-in through
// TLS_TERMINATION=app, which still generates the pair.
func (cfg *TLS) normalizeForDevelopment() {
	if cfg.Termination == "" {
		cfg.Termination = TerminationNone
	}
}

// GeneratesCertificate reports whether the certificate is the throwaway pair
// built at boot, which is what makes dev need no files at all.
func (cfg *TLS) GeneratesCertificate(profile Profile) bool {
	return cfg.ServesTLS() && cfg.CertFile == "" && profile.IsDev()
}

func (cfg *TLS) Validate(profile Profile) error {
	var errs []error

	switch cfg.Termination {
	case TerminationApp:
		errs = append(errs, cfg.validateKeyPair(profile))
	case TerminationProxy, TerminationNone:
		if cfg.CertFile != "" || cfg.KeyFile != "" {
			errs = append(errs, fmt.Errorf(
				"tls: termination %q does not serve TLS, so cert_file and key_file cannot be set",
				cfg.Termination,
			))
		}
	case "":
		errs = append(errs, fmt.Errorf(
			"tls: termination is required: declare %q (this process serves TLS), %q (a gateway does) or %q (nobody does), or run with --dev for a local run",
			TerminationApp, TerminationProxy, TerminationNone,
		))
	default:
		errs = append(errs, fmt.Errorf(
			"tls: unsupported termination %q, want %q, %q or %q",
			cfg.Termination, TerminationApp, TerminationProxy, TerminationNone,
		))
	}

	if cfg.ReloadInterval <= 0 {
		errs = append(errs, fmt.Errorf("tls: reload interval must be greater than zero, got %s", cfg.ReloadInterval))
	}

	return errors.Join(errs...)
}

func (cfg *TLS) validateKeyPair(profile Profile) error {
	if cfg.CertFile == "" && cfg.KeyFile == "" {
		if profile.IsDev() {
			return nil
		}

		return errors.New("tls: termination \"app\" requires cert_file and key_file")
	}

	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return errors.New("tls: cert_file and key_file have to be set together")
	}

	return nil
}
