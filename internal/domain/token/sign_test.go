package token_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"errors"
	"io"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/domain/token"
)

func TestSignRoundTrips(t *testing.T) {
	ec := generateEC(t, elliptic.P256())

	cases := []struct {
		alg    string
		signer crypto.Signer
	}{
		{token.AlgRS256, rsaKey()},
		{token.AlgES256, ec},
	}

	for _, tc := range cases {
		t.Run(tc.alg, func(t *testing.T) {
			want := validClaims()

			compact, err := token.Sign(tc.signer, tc.alg, testKid, want)
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}

			keys := func(kid, alg string) (crypto.PublicKey, error) {
				if kid != testKid || alg != tc.alg {
					t.Errorf("want lookup of %q %q, got %q %q", testKid, tc.alg, kid, alg)
				}

				return tc.signer.Public(), nil
			}

			got, err := token.Verify(compact, keys, midLife)
			if err != nil {
				t.Fatalf("want the token to verify, got %v", err)
			}

			if got.Issuer != want.Issuer || got.Subject != want.Subject ||
				got.SessionID != want.SessionID || got.ID != want.ID {
				t.Errorf("want %+v, got %+v", want, got)
			}

			if !got.IssuedAt.Equal(want.IssuedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
				t.Errorf("want iat %v exp %v, got iat %v exp %v",
					want.IssuedAt, want.ExpiresAt, got.IssuedAt, got.ExpiresAt)
			}
		})
	}
}

func TestSignEmitsExactlyTheExpectedJSON(t *testing.T) {
	claims := validClaims()
	claims.IssuedAt = claims.IssuedAt.Add(700 * time.Millisecond)
	claims.ExpiresAt = claims.ExpiresAt.Add(700 * time.Millisecond)

	compact, err := token.Sign(generateEC(t, elliptic.P256()), token.AlgES256, testKid, claims)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	segments := strings.Split(compact, ".")
	if len(segments) != 3 {
		t.Fatalf("want 3 segments, got %d", len(segments))
	}

	wantHeader := `{"alg":"ES256","kid":"kid-1","typ":"JWT"}`
	if got := string(decode(t, segments[0])); got != wantHeader {
		t.Errorf("want header %s, got %s", wantHeader, got)
	}

	wantPayload := `{"iss":"https://aegis.test/realms/main","sub":"identity-1","sid":"session-1",` +
		`"jti":"token-1","iat":1800000000,"exp":1800000300}`
	if got := string(decode(t, segments[1])); got != wantPayload {
		t.Errorf("want payload %s, got %s", wantPayload, got)
	}
}

// One byte in 256 is zero, so r or s starts with one in about 1 of 128
// signatures: those are the ones a DER-to-raw conversion without padding gets
// wrong.
func TestSignPadsShortES256Halves(t *testing.T) {
	private := generateEC(t, elliptic.P256())

	for range 20000 {
		compact, err := token.Sign(private, token.AlgES256, testKid, validClaims())
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}

		lastDot := strings.LastIndex(compact, ".")
		signature := decode(t, compact[lastDot+1:])

		if len(signature) != 64 {
			t.Fatalf("want a 64 byte signature, got %d", len(signature))
		}

		digest := sha256.Sum256([]byte(compact[:lastDot]))
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])

		if !ecdsa.Verify(&private.PublicKey, digest[:], r, s) {
			t.Fatal("want the two halves to verify as r and s, got a rejection")
		}

		if signature[0] == 0x00 || signature[32] == 0x00 {
			return
		}
	}

	t.Fatal("want a signature with a short r or s, got none in 20000")
}

type stubSigner struct {
	public    crypto.PublicKey
	signature []byte
}

func (s stubSigner) Public() crypto.PublicKey { return s.public }

func (s stubSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return s.signature, nil
}

func TestSignRefusesAMalformedECDSASignature(t *testing.T) {
	public := generateEC(t, elliptic.P256()).Public()

	der := func(r, s *big.Int) []byte {
		encoded, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}

		return encoded
	}

	one := big.NewInt(1)
	tooWide := new(big.Int).Lsh(one, 256)

	cases := map[string][]byte{
		"empty":         nil,
		"not der":       []byte("signature"),
		"raw r and s":   make([]byte, 64),
		"trailing data": append(der(one, one), 0x00),
		"zero r":        der(big.NewInt(0), one),
		"negative s":    der(one, big.NewInt(-1)),
		"r too wide":    der(tooWide, one),
		"s too wide":    der(one, tooWide),
	}

	for name, signature := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := token.Sign(stubSigner{public, signature}, token.AlgES256, testKid, validClaims())
			if err == nil {
				t.Fatal("want an error, got none")
			}
		})
	}
}

func TestSignLeftPadsASmallSignature(t *testing.T) {
	public := generateEC(t, elliptic.P256()).Public()

	der, err := asn1.Marshal(struct{ R, S *big.Int }{big.NewInt(1), big.NewInt(2)})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	compact, err := token.Sign(stubSigner{public, der}, token.AlgES256, testKid, validClaims())
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	want := make([]byte, 64)
	want[31] = 1
	want[63] = 2

	got := decode(t, compact[strings.LastIndex(compact, ".")+1:])
	if string(got) != string(want) {
		t.Errorf("want %x, got %x", want, got)
	}
}

func TestSignRefuses(t *testing.T) {
	p256 := generateEC(t, elliptic.P256())

	with := func(change func(*token.Claims)) token.Claims {
		claims := validClaims()
		change(&claims)

		return claims
	}

	cases := []struct {
		name   string
		signer crypto.Signer
		alg    string
		kid    string
		claims token.Claims
		want   error
	}{
		{"empty issuer", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.Issuer = "" }), token.ErrClaimsInvalid},
		{"empty subject", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.Subject = "" }), token.ErrClaimsInvalid},
		{"empty session id", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.SessionID = "" }), token.ErrClaimsInvalid},
		{"empty token id", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.ID = "" }), token.ErrClaimsInvalid},
		{"zero issue time", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.IssuedAt = time.Time{} }), token.ErrClaimsInvalid},
		{"zero expiry", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.ExpiresAt = time.Time{} }), token.ErrClaimsInvalid},
		{"expiry equal to issue time", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.ExpiresAt = c.IssuedAt }), token.ErrClaimsInvalid},
		{"expiry in the same second", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.ExpiresAt = c.IssuedAt.Add(time.Millisecond) }), token.ErrClaimsInvalid},
		{"expiry before issue time", p256, token.AlgES256, testKid,
			with(func(c *token.Claims) { c.ExpiresAt = c.IssuedAt.Add(-time.Second) }), token.ErrClaimsInvalid},
		{"empty kid", p256, token.AlgES256, "", validClaims(), token.ErrClaimsInvalid},
		{"alg none", p256, "none", testKid, validClaims(), token.ErrUnsupportedAlgorithm},
		{"alg HS256", p256, "HS256", testKid, validClaims(), token.ErrUnsupportedAlgorithm},
		{"alg PS256", rsaKey(), "PS256", testKid, validClaims(), token.ErrUnsupportedAlgorithm},
		{"alg in lower case", rsaKey(), "rs256", testKid, validClaims(), token.ErrUnsupportedAlgorithm},
		{"empty alg", p256, "", testKid, validClaims(), token.ErrUnsupportedAlgorithm},
		{"RS256 with an EC signer", p256, token.AlgRS256, testKid, validClaims(), token.ErrUnsupportedAlgorithm},
		{"ES256 with an RSA signer", rsaKey(), token.AlgES256, testKid, validClaims(), token.ErrUnsupportedAlgorithm},
		{"RS256 with a 1024 bit key", mustGenerateRSA(1024), token.AlgRS256, testKid,
			validClaims(), token.ErrUnsupportedAlgorithm},
		{"ES256 on P-384", generateEC(t, elliptic.P384()), token.AlgES256, testKid,
			validClaims(), token.ErrUnsupportedAlgorithm},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compact, err := token.Sign(tc.signer, tc.alg, tc.kid, tc.claims)
			if !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}

			if compact != "" {
				t.Errorf("want no token, got %q", compact)
			}
		})
	}
}
