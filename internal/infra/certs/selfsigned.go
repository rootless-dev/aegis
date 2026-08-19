// Package certs owns the certificate served on every TLS handshake, from the
// throwaway pair development runs on to the rotating files production is given.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/phuslu/log"
)

// DevelopmentHosts is what a self-signed certificate is issued for: the names a
// developer can actually reach the process at.
var DevelopmentHosts = []string{"localhost", "127.0.0.1", "::1"}

const (
	selfSignedValidity = 30 * 24 * time.Hour

	// Backdated so a client whose clock runs behind still accepts a certificate
	// minted seconds ago.
	selfSignedBackdate = time.Hour
)

// GenerateSelfSigned mints a certificate that never touches the disk: it lives
// as long as the process and is regenerated on the next boot. That is what lets
// development need no configuration at all while still exercising the same TLS
// path production takes.
func GenerateSelfSigned(hosts []string) (*tls.Certificate, error) {
	// P-256 rather than RSA: generation is immediate, and a key pair minted at
	// every boot cannot afford the hundreds of milliseconds RSA would add.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("certs: generating the key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("certs: generating the serial number: %w", err)
	}

	now := time.Now()

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "aegis development", Organization: []string{"aegis"}},
		NotBefore:             now.Add(-selfSignedBackdate),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Names go in the subject alternative name extension and nowhere else: the
	// common name has been ignored by browsers and by Go itself for years, so a
	// certificate carrying only that one fails verification with an error that
	// points nowhere near the cause.
	for _, host := range hosts {
		if address := net.ParseIP(host); address != nil {
			template.IPAddresses = append(template.IPAddresses, address)

			continue
		}

		template.DNSNames = append(template.DNSNames, host)
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("certs: creating the certificate: %w", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("certs: parsing the generated certificate: %w", err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		// Kept so the handshake does not parse the certificate again on every
		// connection.
		Leaf: leaf,
	}, nil
}

// SelfSigned keeps a certificate that only exists in memory. There is nothing to
// reload: no file rotates, and the pair dies with the process.
func SelfSigned(logger *log.Logger, hosts []string) (*Keeper, error) {
	certificate, err := GenerateSelfSigned(hosts)
	if err != nil {
		return nil, err
	}

	logger.Warn().
		Strs("hosts", hosts).
		Time("expires_at", certificate.Leaf.NotAfter).
		Msg("serving a self-signed certificate generated in memory, clients have to skip verification")

	return NewKeeper(logger, certificate), nil
}
