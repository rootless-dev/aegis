package token

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

type signedHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type signedPayload struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Sid string `json:"sid"`
	Jti string `json:"jti"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// Sign returns the compact JWS. alg and kid describe the key behind signer.
func Sign(signer crypto.Signer, alg, kid string, claims Claims) (string, error) {
	if !supported(alg) {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedAlgorithm, alg)
	}

	if kid == "" {
		return "", fmt.Errorf("%w: the kid is empty", ErrClaimsInvalid)
	}

	if err := claims.validate(); err != nil {
		return "", err
	}

	if !keyMatches(alg, signer.Public()) {
		return "", fmt.Errorf("%w: a %T cannot sign %s", ErrUnsupportedAlgorithm, signer.Public(), alg)
	}

	header, err := json.Marshal(signedHeader{Alg: alg, Kid: kid, Typ: typeJWT})
	if err != nil {
		return "", fmt.Errorf("token: encoding the header: %w", err)
	}

	payload, err := json.Marshal(signedPayload{
		Iss: claims.Issuer,
		Sub: claims.Subject,
		Sid: claims.SessionID,
		Jti: claims.ID,
		Iat: claims.IssuedAt.Unix(),
		Exp: claims.ExpiresAt.Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("token: encoding the payload: %w", err)
	}

	signingInput := segmentEncoding.EncodeToString(header) + "." + segmentEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))

	// For an RSA signer, a bare crypto.SHA256 as opts selects PKCS#1 v1.5.
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("token: signing: %w", err)
	}

	if alg == AlgES256 {
		signature, err = fixedWidth(signature)
		if err != nil {
			return "", err
		}
	}

	return signingInput + "." + segmentEncoding.EncodeToString(signature), nil
}

func (c Claims) validate() error {
	switch {
	case c.Issuer == "":
		return fmt.Errorf("%w: the issuer is empty", ErrClaimsInvalid)
	case c.Subject == "":
		return fmt.Errorf("%w: the subject is empty", ErrClaimsInvalid)
	case c.SessionID == "":
		return fmt.Errorf("%w: the session id is empty", ErrClaimsInvalid)
	case c.ID == "":
		return fmt.Errorf("%w: the token id is empty", ErrClaimsInvalid)
	case c.IssuedAt.IsZero() || c.ExpiresAt.IsZero():
		return fmt.Errorf("%w: a time claim is zero", ErrClaimsInvalid)
	case c.ExpiresAt.Unix() <= c.IssuedAt.Unix():
		return fmt.Errorf("%w: the expiry is not after the issue time", ErrClaimsInvalid)
	}

	return nil
}

// A crypto.Signer returns an ECDSA signature as an ASN.1 DER SEQUENCE{r, s};
// JWS requires R‖S, each left-padded to the field size (RFC 7518 section 3.4).
func fixedWidth(der []byte) ([]byte, error) {
	var parsed struct{ R, S *big.Int }

	rest, err := asn1.Unmarshal(der, &parsed)
	if err != nil || len(rest) != 0 {
		return nil, errors.New("token: the signer returned a malformed ecdsa signature")
	}

	// FillBytes panics on a value wider than its buffer.
	for _, value := range []*big.Int{parsed.R, parsed.S} {
		if value.Sign() <= 0 || value.BitLen() > es256HalfSize*8 {
			return nil, errors.New("token: the signer returned an ecdsa signature out of range")
		}
	}

	signature := make([]byte, 2*es256HalfSize)
	parsed.R.FillBytes(signature[:es256HalfSize])
	parsed.S.FillBytes(signature[es256HalfSize:])

	return signature, nil
}
