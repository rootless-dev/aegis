package application

import (
	"crypto/tls"
	"slices"
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
)

// stubCertificateSource is the point of the port: something that is not the
// certs package answering handshakes, without the assembly noticing.
type stubCertificateSource struct {
	certificate *tls.Certificate
	calls       int
}

func (s *stubCertificateSource) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.calls++

	return s.certificate, nil
}

func TestTLSConfigResolvesEachHandshakeThroughTheSource(t *testing.T) {
	source := &stubCertificateSource{certificate: &tls.Certificate{}}

	app := &Application{
		cfg: &configs.Application{
			TLS: &configs.TLS{Termination: configs.TerminationApp},
		},
		certificates: source,
	}

	tlsConfig := app.tlsConfig()
	if tlsConfig == nil {
		t.Fatal("a deployment terminating TLS must produce a TLS configuration")
	}

	// Not configurable, and pinned here so a refactor cannot quietly lower it.
	if tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("min version: want TLS 1.2, got %x", tlsConfig.MinVersion)
	}

	// Without h2 advertised, HTTP/2 disappears with no error at all.
	if !slices.Contains(tlsConfig.NextProtos, "h2") {
		t.Errorf("next protos should advertise h2, got %v", tlsConfig.NextProtos)
	}

	// Resolved per handshake rather than pinned at startup, which is what lets
	// the certificate change under a running server.
	certificate, err := tlsConfig.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("getting the certificate: %v", err)
	}

	if certificate != source.certificate {
		t.Error("the handshake should be answered by the configured source")
	}

	if source.calls != 1 {
		t.Errorf("want one call to the source, got %d", source.calls)
	}
}

func TestTLSConfigIsAbsentWhenSomethingElseTerminates(t *testing.T) {
	app := &Application{
		cfg: &configs.Application{
			TLS: &configs.TLS{Termination: configs.TerminationProxy},
		},
	}

	// A nil configuration is what makes the server listen in plain HTTP.
	if app.tlsConfig() != nil {
		t.Error("no certificate source means no TLS configuration")
	}
}
