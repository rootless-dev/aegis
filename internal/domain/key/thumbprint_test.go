package key_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"testing"

	"github.com/rootless-dev/aegis/internal/domain/key"
)

// From RFC 7638 section 3.1, pasted rather than computed here: a self-computed
// expectation proves only that the code agrees with itself.
const (
	rfc7638N = "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuh" +
		"DR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6C" +
		"f0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n" +
		"91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF" +
		"44-csFCur-kEgU8awapJzKnqDKgw"
	rfc7638E          = "AQAB"
	rfc7638Thumbprint = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"
)

func TestThumbprintMatchesTheRFCVector(t *testing.T) {
	modulus, err := base64.RawURLEncoding.DecodeString(rfc7638N)
	if err != nil {
		t.Fatalf("decoding the vector modulus: %v", err)
	}

	exponent, err := base64.RawURLEncoding.DecodeString(rfc7638E)
	if err != nil {
		t.Fatalf("decoding the vector exponent: %v", err)
	}

	pub := &rsa.PublicKey{
		N: new(big.Int).SetBytes(modulus),
		E: int(new(big.Int).SetBytes(exponent).Int64()),
	}

	got, err := key.Thumbprint(pub)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if got != rfc7638Thumbprint {
		t.Errorf("want %q, got %q", rfc7638Thumbprint, got)
	}
}

func TestThumbprintIsStableForOneKey(t *testing.T) {
	pub := generateECPublic(t)

	first, err := key.Thumbprint(pub)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	second, err := key.Thumbprint(pub)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if first != second {
		t.Errorf("want the same thumbprint twice, got %q and %q", first, second)
	}
}

func TestThumbprintDiffersBetweenKeys(t *testing.T) {
	first, err := key.Thumbprint(generateECPublic(t))
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	second, err := key.Thumbprint(generateECPublic(t))
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if first == second {
		t.Error("want different thumbprints for different keys, got one value")
	}
}

func TestThumbprintRefusesAnUnsupportedKey(t *testing.T) {
	if _, err := key.Thumbprint("not a key"); err == nil {
		t.Fatal("want an error, got none")
	}
}

func generateECPublic(t *testing.T) *ecdsa.PublicKey {
	t.Helper()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	return &private.PublicKey
}
