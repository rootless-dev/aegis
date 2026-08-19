package certs_test

import (
	"crypto/x509"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/infra/certs"
)

// Loading a certificate never looks at the clock: an expired pair loads exactly
// like a fresh one, and the only sign of trouble is every client failing the
// handshake at once.
func TestCheckValidity(t *testing.T) {
	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		notBefore time.Time
		notAfter  time.Time
		wants     certs.Validity
	}{
		"comfortably valid": {
			notBefore: now.Add(-time.Hour),
			notAfter:  now.Add(90 * 24 * time.Hour),
			wants:     certs.ValidityOK,
		},
		"expired yesterday": {
			notBefore: now.Add(-90 * 24 * time.Hour),
			notAfter:  now.Add(-24 * time.Hour),
			wants:     certs.ValidityExpired,
		},
		// An issuer renews well ahead of this, so reaching the window means the
		// renewal itself is failing.
		"inside the warning window": {
			notBefore: now.Add(-90 * 24 * time.Hour),
			notAfter:  now.Add(certs.ExpiryWarning - time.Hour),
			wants:     certs.ValidityExpiringSoon,
		},
		"just outside the window": {
			notBefore: now.Add(-90 * 24 * time.Hour),
			notAfter:  now.Add(certs.ExpiryWarning + time.Hour),
			wants:     certs.ValidityOK,
		},
		// A clock skewed backwards, or a pair installed ahead of its time.
		"not valid yet": {
			notBefore: now.Add(24 * time.Hour),
			notAfter:  now.Add(90 * 24 * time.Hour),
			wants:     certs.ValidityNotYetValid,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			leaf := &x509.Certificate{NotBefore: tc.notBefore, NotAfter: tc.notAfter}

			if got := certs.CheckValidity(leaf, now); got != tc.wants {
				t.Errorf("want %v, got %v", tc.wants, got)
			}
		})
	}
}

func TestCheckValidityToleratesAnAbsentLeaf(t *testing.T) {
	if got := certs.CheckValidity(nil, time.Now()); got != certs.ValidityOK {
		t.Errorf("a missing leaf must not be reported as a date problem, got %v", got)
	}
}
