package certs

import (
	"crypto/x509"
	"time"
)

// ExpiryWarning is how long before the expiry the log starts asking for
// attention. Issuers renew well ahead of that, so a warning this late means the
// renewal itself is failing.
const ExpiryWarning = 14 * 24 * time.Hour

type Validity int

const (
	ValidityOK Validity = iota
	ValidityExpiringSoon
	ValidityExpired
	ValidityNotYetValid
)

func (v Validity) String() string {
	switch v {
	case ValidityExpiringSoon:
		return "expiring soon"
	case ValidityExpired:
		return "expired"
	case ValidityNotYetValid:
		return "not valid yet"
	default:
		return "ok"
	}
}

// CheckValidity compares the certificate dates against the clock, which is the
// one thing loading a certificate never does: a pair that expired last week
// loads exactly like a fresh one, and the only sign of trouble is every client
// suddenly failing the handshake.
func CheckValidity(leaf *x509.Certificate, now time.Time) Validity {
	switch {
	case leaf == nil:
		return ValidityOK
	case now.After(leaf.NotAfter):
		return ValidityExpired
	case now.Before(leaf.NotBefore):
		return ValidityNotYetValid
	case now.Add(ExpiryWarning).After(leaf.NotAfter):
		return ValidityExpiringSoon
	default:
		return ValidityOK
	}
}
