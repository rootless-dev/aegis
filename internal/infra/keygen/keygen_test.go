package keygen_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/infra/keygen"
)

func TestGenerateRS256(t *testing.T) {
	signer, pkcs8, err := keygen.New().Generate(key.AlgorithmRS256)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	private, ok := signer.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("want an rsa key, got %T", signer)
	}

	if private.N.BitLen() != 2048 {
		t.Errorf("want 2048 bits, got %d", private.N.BitLen())
	}

	assertRoundTrip(t, signer, pkcs8)
}

func TestGenerateES256(t *testing.T) {
	signer, pkcs8, err := keygen.New().Generate(key.AlgorithmES256)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	private, ok := signer.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("want an ecdsa key, got %T", signer)
	}

	if private.Curve != elliptic.P256() {
		t.Errorf("want P-256, got %s", private.Curve.Params().Name)
	}

	assertRoundTrip(t, signer, pkcs8)
}

func TestGenerateRefusesAnUnknownAlgorithm(t *testing.T) {
	_, _, err := keygen.New().Generate(key.Algorithm("HS256"))
	if !errors.Is(err, key.ErrUnsupportedAlgorithm) {
		t.Fatalf("want ErrUnsupportedAlgorithm, got %v", err)
	}
}

func TestGenerateProducesDifferentKeys(t *testing.T) {
	_, first, err := keygen.New().Generate(key.AlgorithmES256)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	_, second, err := keygen.New().Generate(key.AlgorithmES256)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	if string(first) == string(second) {
		t.Error("want two different keys, got one")
	}
}

// A mismatch would publish a public key nothing the realm holds can sign for.
func assertRoundTrip(t *testing.T, signer crypto.Signer, pkcs8 []byte) {
	t.Helper()

	parsed, err := x509.ParsePKCS8PrivateKey(pkcs8)
	if err != nil {
		t.Fatalf("parsing the pkcs8: %v", err)
	}

	holder, ok := parsed.(crypto.Signer)
	if !ok {
		t.Fatalf("want a key that can sign, got %T", parsed)
	}

	// Both rsa and ecdsa public keys carry Equal, so one helper covers the two.
	type equalable interface{ Equal(crypto.PublicKey) bool }

	encoded, ok := holder.Public().(equalable)
	if !ok {
		t.Fatalf("want a comparable public key, got %T", holder.Public())
	}

	if !encoded.Equal(signer.Public()) {
		t.Error("want the signer and the pkcs8 to be one key pair, got two")
	}
}
