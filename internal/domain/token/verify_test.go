package token_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/domain/token"
)

const base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

func TestVerifyRejects(t *testing.T) {
	rsaPrivate := rsaKey()
	rsaPublic := &rsaPrivate.PublicKey
	ecPrivate := generateEC(t, elliptic.P256())
	ecPublic := &ecPrivate.PublicKey

	validRS := forge(t, rsaPrivate, header(token.AlgRS256), validPayload())
	validES := forge(t, ecPrivate, header(token.AlgES256), validPayload())
	rsSegments := strings.Split(validRS, ".")
	esSegments := strings.Split(validES, ".")
	esSignature := decode(t, esSegments[2])

	signedRS := func(header, payload string) string { return forge(t, rsaPrivate, header, payload) }
	withESSignature := func(signature []byte) string {
		return esSegments[0] + "." + esSegments[1] + "." + encode(signature)
	}

	cases := []struct {
		name    string
		compact string
		keys    token.KeyFunc
		now     time.Time
		want    error
	}{
		// Algorithm.
		{"alg none with an empty signature",
			encode([]byte(header("none"))) + "." + rsSegments[1] + ".",
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"alg none without the signature segment",
			encode([]byte(header("none"))) + "." + rsSegments[1],
			failingLookup(t), midLife, token.ErrMalformed},
		{"alg none keeping a real signature",
			encode([]byte(header("none"))) + "." + rsSegments[1] + "." + rsSegments[2],
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"alg None in another case",
			encode([]byte(header("None"))) + "." + rsSegments[1] + ".",
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"HS256 keyed with the RSA public key",
			hmacConfusion(t, rsaPrivate.Public()),
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"PS256", signedRS(header("PS256"), validPayload()),
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"alg in lower case", signedRS(header("rs256"), validPayload()),
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"alg under another member case", signedRS(`{"ALG":"RS256","kid":"kid-1"}`, validPayload()),
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"alg missing", signedRS(`{"kid":"kid-1"}`, validPayload()),
			failingLookup(t), midLife, token.ErrUnsupportedAlgorithm},
		{"alg as a number", signedRS(`{"alg":256,"kid":"kid-1"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"alg null", signedRS(`{"alg":null,"kid":"kid-1"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"RS256 token against an EC key", validRS,
			staticKey(ecPublic), midLife, token.ErrUnsupportedAlgorithm},
		{"ES256 token against an RSA key", validES,
			staticKey(rsaPublic), midLife, token.ErrUnsupportedAlgorithm},
		{"ES256 header over an RSA signature",
			esSegments[0] + "." + rsSegments[1] + "." + rsSegments[2],
			staticKey(rsaPublic), midLife, token.ErrUnsupportedAlgorithm},
		{"ES256 against a P-384 key", validES,
			staticKey(generateEC(t, elliptic.P384()).Public()), midLife, token.ErrUnsupportedAlgorithm},
		{"RS256 against a 1024 bit key", validRS,
			staticKey(mustGenerateRSA(1024).Public()), midLife, token.ErrUnsupportedAlgorithm},
		{"a private key from the lookup", validRS,
			staticKey(rsaPrivate), midLife, token.ErrUnsupportedAlgorithm},
		{"a nil key from the lookup", validRS,
			staticKey(nil), midLife, token.ErrUnsupportedAlgorithm},

		// Signature.
		{"tampered payload",
			rsSegments[0] + "." + encode([]byte(payload("1800000000", "1900000000"))) + "." + rsSegments[2],
			staticKey(rsaPublic), midLife, token.ErrSignature},
		{"tampered header",
			encode([]byte(`{"alg":"RS256","kid":"kid-2","typ":"JWT"}`)) + "." + rsSegments[1] + "." + rsSegments[2],
			staticKey(rsaPublic), midLife, token.ErrSignature},
		{"RS256 signature from another key", validRS,
			staticKey(otherRSAKey().Public()), midLife, token.ErrSignature},
		{"ES256 signature from another key", validES,
			staticKey(generateEC(t, elliptic.P256()).Public()), midLife, token.ErrSignature},
		{"empty RS256 signature", rsSegments[0] + "." + rsSegments[1] + ".",
			staticKey(rsaPublic), midLife, token.ErrSignature},
		{"empty ES256 signature", withESSignature(nil),
			staticKey(ecPublic), midLife, token.ErrSignature},
		{"ES256 signature as DER",
			esSegments[0] + "." + esSegments[1] + "." + encode(derSignature(t, ecPrivate, esSegments[0]+"."+esSegments[1])),
			staticKey(ecPublic), midLife, token.ErrSignature},
		{"ES256 signature of 63 bytes", withESSignature(esSignature[:63]),
			staticKey(ecPublic), midLife, token.ErrSignature},
		{"ES256 signature of 65 bytes", withESSignature(append([]byte{0x00}, esSignature...)),
			staticKey(ecPublic), midLife, token.ErrSignature},
		{"ES256 signature of zeros", withESSignature(make([]byte, 64)),
			staticKey(ecPublic), midLife, token.ErrSignature},

		// Serialization.
		{"empty", "", failingLookup(t), midLife, token.ErrMalformed},
		{"one segment", rsSegments[0], failingLookup(t), midLife, token.ErrMalformed},
		{"two segments", rsSegments[0] + "." + rsSegments[1], failingLookup(t), midLife, token.ErrMalformed},
		{"four segments", validRS + "." + rsSegments[2], failingLookup(t), midLife, token.ErrMalformed},
		{"only dots", "..", failingLookup(t), midLife, token.ErrMalformed},
		{"padded signature", validRS + "==", failingLookup(t), midLife, token.ErrMalformed},
		{"padded header", paddedHeader(t) + "." + rsSegments[1] + "." + rsSegments[2],
			failingLookup(t), midLife, token.ErrMalformed},
		{"standard alphabet", rsSegments[0] + "." + rsSegments[1] + "." + "+/" + rsSegments[2][2:],
			failingLookup(t), midLife, token.ErrMalformed},
		{"newline inside a segment", rsSegments[0] + "." + rsSegments[1] + "." + rsSegments[2][:8] + "\n" + rsSegments[2][8:],
			failingLookup(t), midLife, token.ErrMalformed},
		{"carriage return after the token", validRS + "\r",
			failingLookup(t), midLife, token.ErrMalformed},
		{"space before the token", " " + validRS, failingLookup(t), midLife, token.ErrMalformed},
		{"non-canonical trailing bits", nonCanonical(t, validRS),
			failingLookup(t), midLife, token.ErrMalformed},
		{"oversized", signedRS(header(token.AlgRS256), oversizedPayload()),
			failingLookup(t), midLife, token.ErrMalformed},

		// Header.
		{"header that is not json", signedRS(`not json`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header that is an array", signedRS(`["RS256"]`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header left open", signedRS(`{"alg":"RS256","kid":"kid-1"`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"data after the header", signedRS(header(token.AlgRS256)+`{}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"duplicate alg, the real one last", signedRS(`{"alg":"none","alg":"RS256","kid":"kid-1"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"duplicate alg, the real one first", signedRS(`{"alg":"RS256","alg":"none","kid":"kid-1"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"duplicate kid", signedRS(`{"alg":"RS256","kid":"kid-1","kid":"kid-2"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header with jku", signedRS(`{"alg":"RS256","kid":"kid-1","jku":"https://evil.test/jwks"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header with x5u", signedRS(`{"alg":"RS256","kid":"kid-1","x5u":"https://evil.test/cert"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header with jwk", signedRS(`{"alg":"RS256","kid":"kid-1","jwk":{"kty":"RSA"}}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header with x5c", signedRS(`{"alg":"RS256","kid":"kid-1","x5c":["MIIB"]}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header with crit", signedRS(`{"alg":"RS256","kid":"kid-1","crit":["exp"]}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"header with a null jwk", signedRS(`{"alg":"RS256","kid":"kid-1","jwk":null}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"another typ", signedRS(`{"alg":"RS256","kid":"kid-1","typ":"at+jwt"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"typ as a number", signedRS(`{"alg":"RS256","kid":"kid-1","typ":1}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"missing kid", signedRS(`{"alg":"RS256","typ":"JWT"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"empty kid", signedRS(`{"alg":"RS256","kid":"","typ":"JWT"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"kid as a number", signedRS(`{"alg":"RS256","kid":1,"typ":"JWT"}`, validPayload()),
			failingLookup(t), midLife, token.ErrMalformed},
		{"unknown kid", validRS,
			func(string, string) (crypto.PublicKey, error) { return nil, token.ErrKeyUnknown },
			midLife, token.ErrKeyUnknown},

		// Claims, all under a signature that holds.
		{"payload that is not an object", signedRS(header(token.AlgRS256), `"claims"`),
			staticKey(rsaPublic), midLife, token.ErrMalformed},
		{"data after the payload", signedRS(header(token.AlgRS256), validPayload()+`{}`),
			staticKey(rsaPublic), midLife, token.ErrMalformed},
		{"duplicate exp",
			signedRS(header(token.AlgRS256), strings.TrimSuffix(validPayload(), "}")+`,"exp":1900000000}`),
			staticKey(rsaPublic), midLife, token.ErrMalformed},
		{"exp as a string", signedRS(header(token.AlgRS256), payload("1800000000", `"1800000300"`)),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"iat as a string", signedRS(header(token.AlgRS256), payload(`"1800000000"`, "1800000300")),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"exp null", signedRS(header(token.AlgRS256), payload("1800000000", "null")),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"exp true", signedRS(header(token.AlgRS256), payload("1800000000", "true")),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"exp beyond a float", signedRS(header(token.AlgRS256), payload("1800000000", "1e999")),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"exp beyond the calendar", signedRS(header(token.AlgRS256), payload("1800000000", "1e300")),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"negative iat", signedRS(header(token.AlgRS256), payload("-1", "1800000300")),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"missing exp",
			signedRS(header(token.AlgRS256), `{"iss":"i","sub":"s","sid":"d","jti":"j","iat":1800000000}`),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"missing iat",
			signedRS(header(token.AlgRS256), `{"iss":"i","sub":"s","sid":"d","jti":"j","exp":1800000300}`),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"missing sub",
			signedRS(header(token.AlgRS256), `{"iss":"i","sid":"d","jti":"j","iat":1800000000,"exp":1800000300}`),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"empty iss",
			signedRS(header(token.AlgRS256), `{"iss":"","sub":"s","sid":"d","jti":"j","iat":1800000000,"exp":1800000300}`),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"sid as a number",
			signedRS(header(token.AlgRS256), `{"iss":"i","sub":"s","sid":7,"jti":"j","iat":1800000000,"exp":1800000300}`),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"jti null",
			signedRS(header(token.AlgRS256), `{"iss":"i","sub":"s","sid":"d","jti":null,"iat":1800000000,"exp":1800000300}`),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},
		{"nbf as a string",
			signedRS(header(token.AlgRS256), strings.TrimSuffix(validPayload(), "}")+`,"nbf":"1800000000"}`),
			staticKey(rsaPublic), midLife, token.ErrClaimsInvalid},

		// Time.
		{"expired exactly at exp", validRS, staticKey(rsaPublic), expiresAt, token.ErrExpired},
		{"expired long ago", validRS, staticKey(rsaPublic), expiresAt.Add(24 * time.Hour), token.ErrExpired},
		{"expired past a fractional exp",
			signedRS(header(token.AlgRS256), payload("1800000000", "1800000300.5")),
			staticKey(rsaPublic), expiresAt.Add(500 * time.Millisecond), token.ErrExpired},
		{"iat far in the future", validRS, staticKey(rsaPublic), issuedAt.Add(-time.Hour), token.ErrNotYetValid},
		{"iat 61 seconds ahead", validRS, staticKey(rsaPublic), issuedAt.Add(-61 * time.Second), token.ErrNotYetValid},
		{"nbf in the future",
			signedRS(header(token.AlgRS256), strings.TrimSuffix(validPayload(), "}")+`,"nbf":1800000200}`),
			staticKey(rsaPublic), midLife, token.ErrNotYetValid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := token.Verify(tc.compact, tc.keys, tc.now)
			if !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}

			if claims != (token.Claims{}) {
				t.Errorf("want no claims, got %+v", claims)
			}
		})
	}
}

func TestVerifyAccepts(t *testing.T) {
	rsaPrivate := rsaKey()
	ecPrivate := generateEC(t, elliptic.P256())

	signedRS := func(header, payload string) string { return forge(t, rsaPrivate, header, payload) }
	validRS := signedRS(header(token.AlgRS256), validPayload())

	cases := []struct {
		name    string
		compact string
		public  crypto.PublicKey
		now     time.Time
	}{
		{"RS256", validRS, rsaPrivate.Public(), midLife},
		{"ES256", forge(t, ecPrivate, header(token.AlgES256), validPayload()), ecPrivate.Public(), midLife},
		{"one second before exp", validRS, rsaPrivate.Public(), expiresAt.Add(-time.Second)},
		{"one nanosecond before exp", validRS, rsaPrivate.Public(), expiresAt.Add(-time.Nanosecond)},
		{"inside a fractional exp", signedRS(header(token.AlgRS256), payload("1800000000", "1800000300.5")),
			rsaPrivate.Public(), expiresAt.Add(400 * time.Millisecond)},
		{"exp in exponent notation", signedRS(header(token.AlgRS256), payload("1800000000", "1.8000003e9")),
			rsaPrivate.Public(), midLife},
		{"iat 60 seconds ahead", validRS, rsaPrivate.Public(), issuedAt.Add(-60 * time.Second)},
		{"no typ", signedRS(`{"alg":"RS256","kid":"kid-1"}`, validPayload()), rsaPrivate.Public(), midLife},
		{"typ in lower case", signedRS(`{"alg":"RS256","kid":"kid-1","typ":"jwt"}`, validPayload()),
			rsaPrivate.Public(), midLife},
		{"unknown header member", signedRS(`{"alg":"RS256","kid":"kid-1","cty":"x"}`, validPayload()),
			rsaPrivate.Public(), midLife},
		{"unknown claims",
			signedRS(header(token.AlgRS256), strings.TrimSuffix(validPayload(), "}")+`,"aud":["a"],"scope":{"deep":[1,2]}}`),
			rsaPrivate.Public(), midLife},
		{"nbf in the past",
			signedRS(header(token.AlgRS256), strings.TrimSuffix(validPayload(), "}")+`,"nbf":1800000030}`),
			rsaPrivate.Public(), midLife},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := token.Verify(tc.compact, staticKey(tc.public), tc.now)
			if err != nil {
				t.Fatalf("want the token to verify, got %v", err)
			}

			if claims.Issuer != testIssuer || claims.Subject != "identity-1" ||
				claims.SessionID != "session-1" || claims.ID != "token-1" {
				t.Errorf("want the signed claims, got %+v", claims)
			}

			if !claims.IssuedAt.Equal(issuedAt) {
				t.Errorf("want iat %v, got %v", issuedAt, claims.IssuedAt)
			}
		})
	}
}

func TestVerifyDoesNotJudgeTheIssuer(t *testing.T) {
	private := rsaKey()
	forged := strings.Replace(validPayload(), testIssuer, "https://elsewhere.test", 1)

	claims, err := token.Verify(forge(t, private, header(token.AlgRS256), forged), staticKey(private.Public()), midLife)
	if err != nil {
		t.Fatalf("want the token to verify, got %v", err)
	}

	if claims.Issuer != "https://elsewhere.test" {
		t.Errorf("want the issuer handed back untouched, got %q", claims.Issuer)
	}
}

func TestVerifyAcceptsATokenAtTheLengthLimit(t *testing.T) {
	private := rsaKey()

	fitted := func(filler int) string {
		padded := strings.TrimSuffix(validPayload(), "}") + fmt.Sprintf(`,"pad":%q}`, strings.Repeat("a", filler))

		return forge(t, private, header(token.AlgRS256), padded)
	}

	// Three payload bytes are four characters, so stepping by three lands on
	// the limit exactly or one short of it.
	filler := 0
	for len(fitted(filler+3)) <= token.MaxTokenLength {
		filler += 3
	}

	if _, err := token.Verify(fitted(filler), staticKey(private.Public()), midLife); err != nil {
		t.Errorf("want a %d character token to verify, got %v", len(fitted(filler)), err)
	}

	_, err := token.Verify(fitted(filler+3), staticKey(private.Public()), midLife)
	if !errors.Is(err, token.ErrMalformed) {
		t.Errorf("want %v past the limit, got %v", token.ErrMalformed, err)
	}
}

// The classic confusion attack: a verifier that lets the token pick the
// algorithm uses the published RSA key as an HMAC secret.
func hmacConfusion(t *testing.T, public crypto.PublicKey) string {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatalf("encoding the public key: %v", err)
	}

	secret := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	signingInput := encode([]byte(header("HS256"))) + "." + encode([]byte(validPayload()))

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))

	return signingInput + "." + encode(mac.Sum(nil))
}

func derSignature(t *testing.T, private *ecdsa.PrivateKey, signingInput string) []byte {
	t.Helper()

	digest := sha256.Sum256([]byte(signingInput))

	signature, err := ecdsa.SignASN1(rand.Reader, private, digest[:])
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	return signature
}

func paddedHeader(t *testing.T) string {
	t.Helper()

	// 28 bytes leave one over a multiple of three, which padding would fill.
	raw := `{"alg":"RS256","kid":"kid1"}`
	if len(raw)%3 != 1 {
		t.Fatalf("want a header that needs padding, got %d bytes", len(raw))
	}

	return encode([]byte(raw)) + "=="
}

// A 256 byte signature ends in a character with four unused bits; setting one
// keeps the decoded bytes and changes the text.
func nonCanonical(t *testing.T, compact string) string {
	t.Helper()

	last := strings.IndexByte(base64URLAlphabet, compact[len(compact)-1])
	if last < 0 || last&1 != 0 {
		t.Fatalf("want a canonical final character, got %q", compact[len(compact)-1])
	}

	return compact[:len(compact)-1] + string(base64URLAlphabet[last|1])
}

func oversizedPayload() string {
	return strings.TrimSuffix(validPayload(), "}") + fmt.Sprintf(`,"pad":%q}`, strings.Repeat("a", token.MaxTokenLength))
}
