// Package key holds a realm's signing keys. It imports nothing beyond the
// standard library and uuid, and it never sees private material in the clear.
package key

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"fmt"
)

// Algorithm is a JWS "alg" value, and the stored column carries this exact
// string.
type Algorithm string

const (
	AlgorithmRS256 Algorithm = "RS256"
	AlgorithmES256 Algorithm = "ES256"
)

func (a Algorithm) String() string { return string(a) }

func ParseAlgorithm(raw string) (Algorithm, error) {
	switch Algorithm(raw) {
	case AlgorithmRS256:
		return AlgorithmRS256, nil
	case AlgorithmES256:
		return AlgorithmES256, nil
	}

	return "", fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, raw)
}

func (a Algorithm) Matches(pub crypto.PublicKey) bool {
	switch a {
	case AlgorithmRS256:
		_, ok := pub.(*rsa.PublicKey)

		return ok
	case AlgorithmES256:
		// The curve is part of the algorithm: ES256 is P-256 and nothing else.
		ec, ok := pub.(*ecdsa.PublicKey)

		return ok && ec.Curve == elliptic.P256()
	}

	return false
}
