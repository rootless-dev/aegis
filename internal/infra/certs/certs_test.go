package certs_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/infra/certs"
)

func discardLogger() *log.Logger {
	return &log.Logger{Writer: log.IOWriter{Writer: io.Discard}}
}

// writePair puts a usable key pair on disk, which is what the file backed
// keeper expects to find and what a rotation replaces.
func writePair(t *testing.T, dir, commonName string) (string, string) {
	t.Helper()

	certificate, err := certs.GenerateSelfSigned([]string{commonName})
	if err != nil {
		t.Fatalf("generating the pair: %v", err)
	}

	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})

	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatalf("marshalling the key: %v", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})

	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("writing the certificate: %v", err)
	}

	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}

	return certPath, keyPath
}

// The names have to land in the subject alternative name extension: a
// certificate carrying only a common name has been rejected by Go and by every
// browser for years.
func TestSelfSignedCarriesTheNamesAsSubjectAlternativeNames(t *testing.T) {
	certificate, err := certs.GenerateSelfSigned(certs.DevelopmentHosts)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	if certificate.Leaf == nil {
		t.Fatal("the leaf should be kept so handshakes do not parse it again")
	}

	if len(certificate.Leaf.DNSNames) != 1 || certificate.Leaf.DNSNames[0] != "localhost" {
		t.Errorf("dns names: want [localhost], got %v", certificate.Leaf.DNSNames)
	}

	for _, want := range []string{"127.0.0.1", "::1"} {
		found := false

		for _, address := range certificate.Leaf.IPAddresses {
			if address.Equal(net.ParseIP(want)) {
				found = true
			}
		}

		if !found {
			t.Errorf("ip addresses should contain %s, got %v", want, certificate.Leaf.IPAddresses)
		}
	}

	if _, ok := certificate.PrivateKey.(*ecdsa.PrivateKey); !ok {
		t.Errorf("want an ecdsa key, got %T", certificate.PrivateKey)
	}

	if !certificate.Leaf.NotAfter.After(time.Now()) {
		t.Error("the certificate is already expired")
	}
}

// Verification against the generated certificate itself is what a client doing
// -k skips; here it proves the certificate is well formed enough to be trusted
// when it is the whole chain.
func TestSelfSignedVerifiesForItsOwnHost(t *testing.T) {
	certificate, err := certs.GenerateSelfSigned(certs.DevelopmentHosts)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(certificate.Leaf)

	if _, err := certificate.Leaf.Verify(x509.VerifyOptions{DNSName: "localhost", Roots: pool}); err != nil {
		t.Errorf("the generated certificate should verify for localhost: %v", err)
	}
}

func TestFromFilesFailsWhenTheFilesAreUnreadable(t *testing.T) {
	_, _, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       filepath.Join(t.TempDir(), "absent.crt"),
		KeyFile:        filepath.Join(t.TempDir(), "absent.key"),
		ReloadInterval: time.Hour,
	})
	if err == nil {
		t.Fatal("an unreadable pair must fail the boot, not the first handshake")
	}
}

// The whole point of the reload loop: an issuer rewrites the files in place and
// the running process picks the new pair up without a restart.
func TestReloadPicksUpARotatedCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.local")

	keeper, reloader, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       certPath,
		KeyFile:        keyPath,
		ReloadInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	reloader.Start()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := reloader.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	if got := commonName(t, keeper); got != "aegis development" {
		t.Fatalf("subject: want the generated one, got %q", got)
	}

	writePair(t, dir, "second.local")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dnsName(t, keeper) == "second.local" {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Errorf("the rotated certificate was never picked up, still serving %q", dnsName(t, keeper))
}

// A rotation writes two files, and reading between the writes is expected. It
// must not take down a process that still holds a perfectly valid pair.
func TestReloadKeepsTheCurrentPairWhenTheFilesGoBad(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.local")

	keeper, reloader, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       certPath,
		KeyFile:        keyPath,
		ReloadInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	reloader.Start()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = reloader.Shutdown(ctx)
	}()

	if err := os.WriteFile(certPath, []byte("half written"), 0o600); err != nil {
		t.Fatalf("corrupting the certificate: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if got := dnsName(t, keeper); got != "first.local" {
		t.Errorf("the loaded pair should have been kept, got %q", got)
	}
}

// A generated pair has no lifecycle at all: it is served the moment it exists
// and there is nothing to start or stop.
func TestSelfSignedIsServedWithoutAnyLifecycle(t *testing.T) {
	keeper, err := certs.SelfSigned(discardLogger(), certs.DevelopmentHosts)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	if commonName(t, keeper) != "aegis development" {
		t.Errorf("subject: got %q", commonName(t, keeper))
	}
}

// The assembly starts every resource once, but a contract that only holds by
// convention is a trap waiting for the day the assembly changes.
func TestReloaderSurvivesRepeatedStartAndEarlyShutdown(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.local")

	_, reloader, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       certPath,
		KeyFile:        keyPath,
		ReloadInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	first := reloader.Start()

	if second := reloader.Start(); second != first {
		t.Error("starting twice should hand back the same channel, not launch another loop")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := reloader.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

// Shutting down something that was never started has to return, not wait for a
// loop that does not exist.
func TestReloaderShutdownBeforeStartReturnsAtOnce(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "first.local")

	_, reloader, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       certPath,
		KeyFile:        keyPath,
		ReloadInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := reloader.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func leaf(t *testing.T, keeper *certs.Keeper) *x509.Certificate {
	t.Helper()

	certificate, err := keeper.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("getting the certificate: %v", err)
	}

	return certificate.Leaf
}

func commonName(t *testing.T, keeper *certs.Keeper) string {
	t.Helper()

	return leaf(t, keeper).Subject.CommonName
}

func dnsName(t *testing.T, keeper *certs.Keeper) string {
	t.Helper()

	names := leaf(t, keeper).DNSNames
	if len(names) == 0 {
		return ""
	}

	return names[0]
}

// A certificate that does not cover the public host completes the handshake
// here and is rejected there, so it has to fail while somebody is still looking
// at the boot.
func TestFromFilesRefusesACertificateThatMissesTheHost(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "other.example.com")

	_, _, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       certPath,
		KeyFile:        keyPath,
		ReloadInterval: time.Hour,
		Hostname:       "auth.example.com",
	})
	if err == nil {
		t.Fatal("a certificate for another host must fail the boot")
	}

	if !strings.Contains(err.Error(), "auth.example.com") {
		t.Errorf("the error should name the host clients use, got %v", err)
	}
}

func TestFromFilesAcceptsACertificateThatCoversTheHost(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "auth.example.com")

	if _, _, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       certPath,
		KeyFile:        keyPath,
		ReloadInterval: time.Hour,
		Hostname:       "auth.example.com",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A rotation that installs the wrong certificate is worse than one that did not
// happen: the pair in use still works, and swapping it would break every client
// at once.
func TestReloadRejectsARotationThatMissesTheHost(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writePair(t, dir, "auth.example.com")

	keeper, reloader, err := certs.FromFiles(certs.Options{
		Logger:         discardLogger(),
		CertFile:       certPath,
		KeyFile:        keyPath,
		ReloadInterval: 10 * time.Millisecond,
		Hostname:       "auth.example.com",
	})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	reloader.Start()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = reloader.Shutdown(ctx)
	}()

	writePair(t, dir, "somewhere.else.com")
	time.Sleep(100 * time.Millisecond)

	if got := dnsName(t, keeper); got != "auth.example.com" {
		t.Errorf("the working pair should have been kept, now serving %q", got)
	}
}

// One certificate is served whatever name is asked for. Pinning it down marks
// where per-host certificates will have to plug in.
func TestOneCertificateAnswersEveryServerName(t *testing.T) {
	keeper, err := certs.SelfSigned(discardLogger(), certs.DevelopmentHosts)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	for _, name := range []string{"localhost", "anything.example.com", ""} {
		certificate, err := keeper.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatalf("server name %q: %v", name, err)
		}

		if certificate.Leaf.DNSNames[0] != "localhost" {
			t.Errorf("server name %q: got a different certificate", name)
		}
	}
}
