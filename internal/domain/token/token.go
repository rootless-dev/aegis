// Package token builds and verifies the aegis access token: a JWT (RFC 7519)
// signed as a compact JWS (RFC 7515). It imports nothing beyond the standard
// library.
package token

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"time"
)

var (
	ErrMalformed            = errors.New("token: the token is malformed")
	ErrUnsupportedAlgorithm = errors.New("token: the algorithm is not supported")
	ErrKeyUnknown           = errors.New("token: the key is unknown")
	ErrSignature            = errors.New("token: the signature does not verify")
	ErrExpired              = errors.New("token: the token has expired")
	ErrNotYetValid          = errors.New("token: the token is not yet valid")
	ErrClaimsInvalid        = errors.New("token: the claims are invalid")
)

const (
	AlgRS256 = "RS256"
	AlgES256 = "ES256"
)

const (
	typeJWT    = "JWT"
	minRSABits = 2048

	// The width of one P-256 field element, and so of each half of an ES256
	// signature.
	es256HalfSize = 32
)

// Claims is what an aegis access token carries.
type Claims struct {
	Issuer    string    // iss — the realm's stored issuer, byte for byte
	Subject   string    // sub — the identity id
	SessionID string    // sid
	ID        string    // jti
	IssuedAt  time.Time // iat
	ExpiresAt time.Time // exp
}

var segmentEncoding = base64.RawURLEncoding.Strict()

func supported(alg string) bool {
	return alg == AlgRS256 || alg == AlgES256
}

// The curve is part of the algorithm: ES256 is P-256 and nothing else.
func keyMatches(alg string, pub crypto.PublicKey) bool {
	switch typed := pub.(type) {
	case *rsa.PublicKey:
		return alg == AlgRS256 && typed != nil && typed.N != nil && typed.N.BitLen() >= minRSABits
	case *ecdsa.PublicKey:
		return alg == AlgES256 && typed != nil && typed.Curve == elliptic.P256()
	}

	return false
}
