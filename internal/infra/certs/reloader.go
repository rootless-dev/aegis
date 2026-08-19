package certs

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phuslu/log"
)

type Options struct {
	Logger   *log.Logger
	CertFile string
	KeyFile  string

	// ReloadInterval is how often the pair is read again. Issuers rotate the
	// files in place, so a certificate loaded once at boot would only be
	// renewed by restarting the process.
	ReloadInterval time.Duration

	// Hostname is the name clients reach this deployment at. A certificate that
	// does not cover it completes the handshake here and is rejected there, so
	// it is checked while somebody is still looking at the boot.
	Hostname string
}

// Reloader re-reads the key pair and hands it to the keeper. It is a resource
// of its own precisely because it has a lifecycle the keeper does not: nothing
// has to be started or stopped to serve a certificate, only to keep it current.
type Reloader struct {
	logger   *log.Logger
	keeper   *Keeper
	certFile string
	keyFile  string
	interval time.Duration
	hostname string

	failure  chan error
	stop     chan struct{}
	finished chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	started   atomic.Bool
}

// FromFiles loads the pair now, so an unreadable certificate fails the boot
// rather than the first handshake, and returns what serves it alongside what
// keeps it fresh.
func FromFiles(opts Options) (*Keeper, *Reloader, error) {
	certificate, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("certs: loading the key pair: %w", err)
	}

	if err := coversHostname(&certificate, opts.Hostname); err != nil {
		return nil, nil, err
	}

	keeper := NewKeeper(opts.Logger, &certificate)

	reloader := &Reloader{
		logger:   opts.Logger,
		keeper:   keeper,
		certFile: opts.CertFile,
		keyFile:  opts.KeyFile,
		interval: opts.ReloadInterval,
		hostname: opts.Hostname,
		failure:  make(chan error),
		stop:     make(chan struct{}),
		finished: make(chan struct{}),
	}

	reloader.logLoaded(&certificate, "certificate loaded")

	return keeper, reloader, nil
}

// Start begins the reload loop. The returned channel carries no error on
// purpose: a rotation caught mid-write must not take down a process that is
// still serving a perfectly valid certificate.
func (r *Reloader) Start() <-chan error {
	r.startOnce.Do(func() {
		r.started.Store(true)

		go r.loop()
	})

	return r.failure
}

func (r *Reloader) Shutdown(ctx context.Context) error {
	r.stopOnce.Do(func() { close(r.stop) })

	// A loop that never started leaves nothing to wait for. Without this, a
	// shutdown ordered before the start would block until the context expires.
	if !r.started.Load() {
		return nil
	}

	select {
	case <-r.finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Reloader) loop() {
	defer close(r.failure)
	defer close(r.finished)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info().Dur("interval", r.interval).Msg("watching the certificate for rotation")

	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.reload()
		}
	}
}

func (r *Reloader) reload() {
	certificate, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		// The loaded pair keeps being served: a rotation writes two files, and
		// reading between the writes is an expected transient failure, not a
		// reason to stop answering handshakes.
		r.logger.Error().Err(err).
			Str("cert_file", r.certFile).
			Msg("reloading the certificate failed, keeping the one in use")

		return
	}

	if current := r.keeper.Current(); current != nil && sameCertificate(current, &certificate) {
		return
	}

	// A rotation that installs the wrong certificate is worse than one that did
	// not happen: the pair in use still works, and swapping it for one nobody
	// can verify would break every client at once.
	if err := coversHostname(&certificate, r.hostname); err != nil {
		r.logger.Error().Err(err).
			Str("cert_file", r.certFile).
			Msg("the rotated certificate was rejected, keeping the one in use")

		return
	}

	r.keeper.Store(&certificate)
	r.logLoaded(&certificate, "certificate reloaded")
}

func coversHostname(certificate *tls.Certificate, hostname string) error {
	if hostname == "" || certificate.Leaf == nil {
		return nil
	}

	if err := certificate.Leaf.VerifyHostname(hostname); err != nil {
		return fmt.Errorf(
			"certs: the certificate does not cover %q, the host clients reach this deployment at: %w", hostname, err,
		)
	}

	return nil
}

// logLoaded reports the pair at the level its dates deserve. An expired
// certificate is still served — refusing to answer at all is not an
// improvement — but it cannot be something the log mentions in passing.
func (r *Reloader) logLoaded(certificate *tls.Certificate, message string) {
	leaf := certificate.Leaf
	validity := CheckValidity(leaf, time.Now())

	entry := r.logger.Info()

	switch validity {
	case ValidityExpired, ValidityNotYetValid:
		entry = r.logger.Error()
		message = "certificate is " + validity.String() + ", clients will reject it"
	case ValidityExpiringSoon:
		entry = r.logger.Warn()
		message = "certificate expires soon and has not been renewed"
	case ValidityOK:
	}

	entry = entry.Str("cert_file", r.certFile)

	if leaf != nil {
		entry = entry.
			Time("expires_at", leaf.NotAfter).
			Strs("covers", leaf.DNSNames)
	}

	entry.Msg(message)
}

// sameCertificate compares the encoded chain rather than the expiry, so a pair
// reissued with identical dates is still recognized as new.
func sameCertificate(a, b *tls.Certificate) bool {
	if len(a.Certificate) != len(b.Certificate) {
		return false
	}

	for i := range a.Certificate {
		if !bytes.Equal(a.Certificate[i], b.Certificate[i]) {
			return false
		}
	}

	return true
}
