package certs

import (
	"crypto/tls"
	"errors"
	"sync/atomic"

	"github.com/phuslu/log"
)

var ErrNoCertificate = errors.New("certs: no certificate is loaded")

// Keeper owns the certificate handed to every handshake and replaces it whole,
// which is why the pointer is atomic: a swap lands between two connections
// without either of them seeing a half-updated pair.
//
// It knows nothing about where the certificate came from, and that is what lets
// a pair generated in memory and one rotating on disk reach the server through
// the same path.
type Keeper struct {
	logger  *log.Logger
	current atomic.Pointer[tls.Certificate]
}

func NewKeeper(logger *log.Logger, certificate *tls.Certificate) *Keeper {
	keeper := &Keeper{logger: logger}
	keeper.Store(certificate)

	return keeper
}

// GetCertificate answers each handshake with whatever is currently loaded.
//
// One certificate is served to every client, whatever name they asked for. That
// is a ceiling worth knowing about: a realm reached at a hostname of its own
// will need a certificate of its own, and this is where choosing among them
// belongs. Until then the name is only read to explain the failure the client
// is about to see.
func (k *Keeper) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	certificate := k.Current()
	if certificate == nil {
		return nil, ErrNoCertificate
	}

	k.reportUncoveredName(hello, certificate)

	return certificate, nil
}

// reportUncoveredName logs the handshake a client is about to reject, which is
// otherwise invisible from this side: the connection completes here and fails
// there. Only mismatches are logged, and at debug, so a scanner asking for
// random names cannot flood anything.
func (k *Keeper) reportUncoveredName(hello *tls.ClientHelloInfo, certificate *tls.Certificate) {
	if hello == nil || hello.ServerName == "" || certificate.Leaf == nil || k.logger == nil {
		return
	}

	if err := certificate.Leaf.VerifyHostname(hello.ServerName); err != nil {
		k.logger.Debug().
			Str("server_name", hello.ServerName).
			Strs("covered", certificate.Leaf.DNSNames).
			Msg("the certificate served does not cover the name the client asked for")
	}
}

func (k *Keeper) Store(certificate *tls.Certificate) {
	k.current.Store(certificate)
}

func (k *Keeper) Current() *tls.Certificate {
	return k.current.Load()
}
