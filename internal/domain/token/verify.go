package token

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const (
	maxTokenLength = 4096
	maxClockSkew   = 60 * time.Second

	// 9999-12-31T23:59:59Z: past it time.Unix stops being trustworthy.
	maxNumericDate = 253402300799
)

// Nothing here resolves a key from the token itself, and "crit" names
// extensions that must be understood to accept the token (RFC 7515 section
// 4.1.11).
var refusedHeaders = []string{"crit", "jku", "jwk", "x5u", "x5c"}

// KeyFunc resolves the public key a token names. It receives the kid and alg
// from the token's header and returns ErrKeyUnknown when it has none.
type KeyFunc func(kid, alg string) (crypto.PublicKey, error)

// Verify checks the signature and the time claims and returns the claims. It
// does not judge the issuer: the caller compares it with the realm's.
func Verify(compact string, keys KeyFunc, now time.Time) (Claims, error) {
	if len(compact) > maxTokenLength {
		return Claims{}, ErrMalformed
	}

	segments := strings.Split(compact, ".")
	if len(segments) != 3 {
		return Claims{}, ErrMalformed
	}

	decoded := make([][]byte, len(segments))

	for i, segment := range segments {
		var err error

		decoded[i], err = decodeSegment(segment)
		if err != nil {
			return Claims{}, err
		}
	}

	header, err := parseObject(decoded[0])
	if err != nil {
		return Claims{}, err
	}

	alg, kid, err := readHeader(header)
	if err != nil {
		return Claims{}, err
	}

	pub, err := keys(kid, alg)
	if err != nil {
		return Claims{}, err
	}

	signingInput := segments[0] + "." + segments[1]

	if err := verifySignature(alg, pub, signingInput, decoded[2]); err != nil {
		return Claims{}, err
	}

	payload, err := parseObject(decoded[1])
	if err != nil {
		return Claims{}, err
	}

	return readClaims(payload, now)
}

func decodeSegment(segment string) ([]byte, error) {
	// The base64 decoder skips \r and \n, even in strict mode.
	if strings.ContainsAny(segment, "\r\n") {
		return nil, ErrMalformed
	}

	decoded, err := segmentEncoding.DecodeString(segment)
	if err != nil {
		return nil, ErrMalformed
	}

	return decoded, nil
}

// Unmarshalling into a struct or a map keeps the last of two members with the
// same name, and a struct also matches names case-insensitively: both let two
// parsers disagree about one token, so the members are walked one by one.
func parseObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))

	if opening, err := decoder.Token(); err != nil || opening != json.Delim('{') {
		return nil, ErrMalformed
	}

	members := make(map[string]json.RawMessage)

	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrMalformed
		}

		name, ok := token.(string)
		if !ok {
			return nil, ErrMalformed
		}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrMalformed
		}

		if _, duplicate := members[name]; duplicate {
			return nil, ErrMalformed
		}

		members[name] = value
	}

	if _, err := decoder.Token(); err != nil {
		return nil, ErrMalformed
	}

	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrMalformed
	}

	return members, nil
}

func readHeader(header map[string]json.RawMessage) (alg, kid string, err error) {
	alg, ok := stringMember(header, "alg")
	if !ok {
		return "", "", ErrMalformed
	}

	// RFC 8725 section 3.1: "none", HMAC and the rest are refused before any
	// key is looked up.
	if !supported(alg) {
		return "", "", ErrUnsupportedAlgorithm
	}

	for _, name := range refusedHeaders {
		if _, present := header[name]; present {
			return "", "", ErrMalformed
		}
	}

	if _, present := header["typ"]; present {
		typ, ok := stringMember(header, "typ")
		if !ok || !strings.EqualFold(typ, typeJWT) {
			return "", "", ErrMalformed
		}
	}

	kid, ok = stringMember(header, "kid")
	if !ok || kid == "" {
		return "", "", ErrMalformed
	}

	return alg, kid, nil
}

// The algorithm is decided by the key the server holds, never by the token
// alone: a key of the other type is refused rather than tried.
func verifySignature(alg string, pub crypto.PublicKey, signingInput string, signature []byte) error {
	if !keyMatches(alg, pub) {
		return ErrUnsupportedAlgorithm
	}

	digest := sha256.Sum256([]byte(signingInput))

	switch typed := pub.(type) {
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(typed, crypto.SHA256, digest[:], signature); err != nil {
			return ErrSignature
		}
	case *ecdsa.PublicKey:
		if len(signature) != 2*es256HalfSize {
			return ErrSignature
		}

		r := new(big.Int).SetBytes(signature[:es256HalfSize])
		s := new(big.Int).SetBytes(signature[es256HalfSize:])

		if !ecdsa.Verify(typed, digest[:], r, s) {
			return ErrSignature
		}
	}

	return nil
}

func readClaims(payload map[string]json.RawMessage, now time.Time) (Claims, error) {
	var (
		claims Claims
		ok     bool
	)

	for name, target := range map[string]*string{
		"iss": &claims.Issuer,
		"sub": &claims.Subject,
		"sid": &claims.SessionID,
		"jti": &claims.ID,
	} {
		*target, ok = stringMember(payload, name)
		if !ok || *target == "" {
			return Claims{}, ErrClaimsInvalid
		}
	}

	if claims.IssuedAt, ok = numericDate(payload["iat"]); !ok {
		return Claims{}, ErrClaimsInvalid
	}

	if claims.ExpiresAt, ok = numericDate(payload["exp"]); !ok {
		return Claims{}, ErrClaimsInvalid
	}

	notBefore := claims.IssuedAt

	if raw, present := payload["nbf"]; present {
		if notBefore, ok = numericDate(raw); !ok {
			return Claims{}, ErrClaimsInvalid
		}
	}

	// exp is exclusive: RFC 7519 section 4.1.4 requires now to be before it.
	if !now.Before(claims.ExpiresAt) {
		return Claims{}, ErrExpired
	}

	horizon := now.Add(maxClockSkew)
	if claims.IssuedAt.After(horizon) || notBefore.After(horizon) {
		return Claims{}, ErrNotYetValid
	}

	return claims, nil
}

// An absent member reads as "", true; ok is false only for a member of
// another JSON type. Unmarshalling null into a string succeeds silently, hence
// the look at the first byte.
func stringMember(members map[string]json.RawMessage, name string) (value string, ok bool) {
	raw, present := members[name]
	if !present {
		return "", true
	}

	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}

	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}

	return value, true
}

// A NumericDate may carry a fraction (RFC 7519 section 2). ParseFloat reports
// an overflowing literal such as 1e999 as an error, so nothing non-finite
// gets through.
func numericDate(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return time.Time{}, false
	}

	seconds, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || seconds < 0 || seconds > maxNumericDate {
		return time.Time{}, false
	}

	whole, fraction := math.Modf(seconds)

	return time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC(), true
}
