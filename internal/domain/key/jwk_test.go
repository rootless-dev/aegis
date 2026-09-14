package key_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
)

func TestJWKCarriesTheRSAMembers(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	built := newKey(t, key.AlgorithmRS256, &private.PublicKey)

	jwk, err := built.JWK()
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if jwk.Kty != "RSA" {
		t.Errorf("want kty RSA, got %q", jwk.Kty)
	}

	if jwk.Use != "sig" {
		t.Errorf("want use sig, got %q", jwk.Use)
	}

	if jwk.Alg != "RS256" {
		t.Errorf("want alg RS256, got %q", jwk.Alg)
	}

	if jwk.Kid != built.KID() {
		t.Errorf("want kid %q, got %q", built.KID(), jwk.Kid)
	}

	// The modulus is minimal big-endian: a leading zero byte would be a
	// different number to every verifier that reads it.
	decoded, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		t.Fatalf("decoding n: %v", err)
	}

	if len(decoded) > 0 && decoded[0] == 0x00 {
		t.Error("want a minimal modulus, got a leading zero byte")
	}

	if new(big.Int).SetBytes(decoded).Cmp(private.N) != 0 {
		t.Error("want n to round trip to the modulus, got a different number")
	}

	if jwk.Crv != "" || jwk.X != "" || jwk.Y != "" {
		t.Error("want no EC members on an RSA key, got some")
	}
}

func TestJWKPadsECCoordinates(t *testing.T) {
	// Only a coordinate that legitimately starts with a zero byte catches a
	// trimming bug, and a random key almost never has one.
	private := generateECWithLeadingZero(t)

	built := newKey(t, key.AlgorithmES256, &private.PublicKey)

	jwk, err := built.JWK()
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if jwk.Kty != "EC" || jwk.Crv != "P-256" {
		t.Errorf("want an EC P-256 jwk, got kty %q crv %q", jwk.Kty, jwk.Crv)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		t.Fatalf("decoding x: %v", err)
	}

	if len(decoded) != 32 {
		t.Errorf("want 32 bytes for a P-256 coordinate, got %d", len(decoded))
	}

	// Length alone does not pin the side: padding on the right also yields 32
	// bytes, and yields a different number. Reading the coordinate back is what
	// separates the two, and it only separates them on a key whose coordinate
	// is short — which is why this test generates one.
	if new(big.Int).SetBytes(decoded).Cmp(private.X) != 0 {
		t.Error("want x to read back as the coordinate, got a different number")
	}

	decodedY, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		t.Fatalf("decoding y: %v", err)
	}

	if new(big.Int).SetBytes(decodedY).Cmp(private.Y) != 0 {
		t.Error("want y to read back as the coordinate, got a different number")
	}

	if jwk.N != "" || jwk.E != "" {
		t.Error("want no RSA members on an EC key, got some")
	}
}

func TestJWKSerializesWithTheSpecMemberNames(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	jwk, err := newKey(t, key.AlgorithmES256, &private.PublicKey).JWK()
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	encoded, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}

	for _, member := range []string{"kty", "use", "alg", "kid", "crv", "x", "y"} {
		if _, ok := decoded[member]; !ok {
			t.Errorf("want member %q present, got it missing", member)
		}
	}

	for _, member := range []string{"n", "e"} {
		if _, ok := decoded[member]; ok {
			t.Errorf("want member %q absent on an EC key, got it present", member)
		}
	}
}

func generateECWithLeadingZero(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	// 1 in 256 keys, so this converges quickly; the bound is there so a broken
	// generator fails the test instead of hanging it.
	for attempt := 0; attempt < 10000; attempt++ {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generating a key: %v", err)
		}

		if len(private.X.Bytes()) < 32 {
			return private
		}
	}

	t.Fatal("no key with a short coordinate in 10000 attempts")

	return nil
}

func newKey(t *testing.T, algorithm key.Algorithm, public any) *key.Key {
	t.Helper()

	built, err := key.New(uuid.New(), key.PurposeSignature, algorithm, public, []byte("sealed"), "kek")
	if err != nil {
		t.Fatalf("building a key: %v", err)
	}

	return built
}
