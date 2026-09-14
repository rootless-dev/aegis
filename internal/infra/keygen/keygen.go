// Package keygen produces the key pairs a realm signs with.
package keygen

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"fmt"

	"github.com/rootless-dev/aegis/internal/domain/key"
)

// The floor every current guideline allows for an RSA signing key.
const rsaBits = 2048

type Generator struct{}

func New() Generator { return Generator{} }

// Generate returns the signer and the PKCS#8 DER of its private half.
func (Generator) Generate(algorithm key.Algorithm) (crypto.Signer, []byte, error) {
	var signer crypto.Signer

	switch algorithm {
	case key.AlgorithmRS256:
		private, err := rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return nil, nil, fmt.Errorf("keygen: generating an rsa key: %w", err)
		}

		signer = private
	case key.AlgorithmES256:
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, fmt.Errorf("keygen: generating an ec key: %w", err)
		}

		signer = private
	default:
		return nil, nil, fmt.Errorf("%w: %q", key.ErrUnsupportedAlgorithm, algorithm)
	}

	pkcs8, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, nil, fmt.Errorf("keygen: marshalling the private key: %w", err)
	}

	return signer, pkcs8, nil
}
