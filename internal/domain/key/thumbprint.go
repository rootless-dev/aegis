package key

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
)

// Thumbprint is the RFC 7638 thumbprint of pub. Marshalling a map gives the
// canonical form section 3.3 requires for free: sorted member names, no
// whitespace.
func Thumbprint(pub crypto.PublicKey) (string, error) {
	members, err := requiredMembers(pub)
	if err != nil {
		return "", err
	}

	canonical, err := json.Marshal(members)
	if err != nil {
		return "", fmt.Errorf("key: building the canonical jwk: %w", err)
	}

	sum := sha256.Sum256(canonical)

	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// The single source both the thumbprint and the published JWK read from: two
// constructions would drift, and the kid would stop identifying the key it
// travels with.
func requiredMembers(pub crypto.PublicKey) (map[string]string, error) {
	switch typed := pub.(type) {
	case *rsa.PublicKey:
		return map[string]string{
			"e":   encodeUnsigned(big.NewInt(int64(typed.E))),
			"kty": "RSA",
			"n":   encodeUnsigned(typed.N),
		}, nil
	case *ecdsa.PublicKey:
		if typed.Curve != elliptic.P256() {
			return nil, fmt.Errorf("key: %s is not a supported curve", typed.Curve.Params().Name)
		}

		size := coordinateSize(typed.Curve)

		return map[string]string{
			"crv": "P-256",
			"kty": "EC",
			"x":   encodeCoordinate(typed.X, size),
			"y":   encodeCoordinate(typed.Y, size),
		}, nil
	}

	return nil, fmt.Errorf("key: %T is not a supported public key", pub)
}

// JWA requires the minimal big-endian form for RSA n and e: no leading zero
// byte, which is what big.Int.Bytes already produces.
func encodeUnsigned(value *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(value.Bytes())
}

// EC coordinates are fixed-width, the opposite rule from RSA above:
// big.Int.Bytes would trim a leading zero and produce a JWK that verifies
// nothing, so FillBytes pads to the field size.
func encodeCoordinate(value *big.Int, size int) string {
	padded := make([]byte, size)
	value.FillBytes(padded)

	return base64.RawURLEncoding.EncodeToString(padded)
}

func coordinateSize(curve elliptic.Curve) int {
	return (curve.Params().BitSize + 7) / 8
}
