package application

import "crypto/tls"

// The interfaces the assembly needs are declared here rather than in the
// packages that satisfy them: the consumer is what knows the shape it depends
// on, and an implementation that never imports its own abstraction is free to
// be replaced without either side being edited.
//
// Resource, in resources.go, is the other one — it happens to live next to the
// list it drives.

// CertificateSource is asked for a certificate on every TLS handshake. The
// signature is the callback net/http already resolves per connection, which is
// what makes the source replaceable while a server is running rather than only
// at boot.
//
// Today it is a pair loaded from files or one generated in memory, both from
// internal/infra/certs. It is an interface because the ones ahead — a KMS, an
// ACME client, a mesh handing certificates out — differ in where the key lives
// and in nothing else this side can see.
type CertificateSource interface {
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}
