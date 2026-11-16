package token_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/domain/token"
)

const (
	testKid    = "kid-1"
	testIssuer = "https://aegis.test/realms/main"
)

var (
	issuedAt  = time.Unix(1_800_000_000, 0)
	expiresAt = issuedAt.Add(5 * time.Minute)
	midLife   = issuedAt.Add(time.Minute)
)

// RSA generation is slow, so each key is made once per test binary.
var (
	rsaKey      = sync.OnceValue(func() *rsa.PrivateKey { return mustGenerateRSA(2048) })
	otherRSAKey = sync.OnceValue(func() *rsa.PrivateKey { return mustGenerateRSA(2048) })
)

func mustGenerateRSA(bits int) *rsa.PrivateKey {
	private, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		panic(err)
	}

	return private
}

func generateEC(t testing.TB, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()

	private, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	return private
}

func validClaims() token.Claims {
	return token.Claims{
		Issuer:    testIssuer,
		Subject:   "identity-1",
		SessionID: "session-1",
		ID:        "token-1",
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
	}
}

func header(alg string) string {
	return fmt.Sprintf(`{"alg":%q,"kid":%q,"typ":"JWT"}`, alg, testKid)
}

// iat and exp are spliced in raw so a case can make them strings or fractions.
func payload(iat, exp string) string {
	return fmt.Sprintf(
		`{"iss":%q,"sub":"identity-1","sid":"session-1","jti":"token-1","iat":%s,"exp":%s}`,
		testIssuer, iat, exp,
	)
}

func validPayload() string {
	return payload("1800000000", "1800000300")
}

func encode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func decode(t testing.TB, segment string) []byte {
	t.Helper()

	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decoding a segment: %v", err)
	}

	return decoded
}

// forge signs arbitrary header and payload JSON with a real key, without going
// through token.Sign: the rejections below need tokens Sign refuses to build.
func forge(t testing.TB, private crypto.Signer, header, payload string) string {
	t.Helper()

	signingInput := encode([]byte(header)) + "." + encode([]byte(payload))

	return signingInput + "." + encode(rawSignature(t, private, signingInput))
}

func rawSignature(t testing.TB, private crypto.Signer, signingInput string) []byte {
	t.Helper()

	digest := sha256.Sum256([]byte(signingInput))

	switch typed := private.(type) {
	case *rsa.PrivateKey:
		signature, err := rsa.SignPKCS1v15(rand.Reader, typed, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatalf("signing: %v", err)
		}

		return signature
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, typed, digest[:])
		if err != nil {
			t.Fatalf("signing: %v", err)
		}

		signature := make([]byte, 64)
		r.FillBytes(signature[:32])
		s.FillBytes(signature[32:])

		return signature
	}

	t.Fatalf("no signature for %T", private)

	return nil
}

func staticKey(pub crypto.PublicKey) token.KeyFunc {
	return func(string, string) (crypto.PublicKey, error) { return pub, nil }
}

func failingLookup(t testing.TB) token.KeyFunc {
	t.Helper()

	return func(kid, alg string) (crypto.PublicKey, error) {
		t.Errorf("want no key lookup, got one for kid %q alg %q", kid, alg)

		return nil, token.ErrKeyUnknown
	}
}
