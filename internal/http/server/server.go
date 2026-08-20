package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/phuslu/log"
)

type Options struct {
	Address string
	Handler http.Handler
	Logger  *log.Logger

	// TLSConfig turns the listener into an HTTPS one. Nil means plain HTTP,
	// which is only ever the case when something in front terminates TLS.
	TLSConfig *tls.Config

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

type Server struct {
	logger *log.Logger
	server *http.Server
}

func New(opts Options) *Server {
	return &Server{
		logger: opts.Logger,
		server: &http.Server{
			Addr:              opts.Address,
			Handler:           opts.Handler,
			TLSConfig:         opts.TLSConfig,
			ReadHeaderTimeout: opts.ReadHeaderTimeout,
			ReadTimeout:       opts.ReadTimeout,
			WriteTimeout:      opts.WriteTimeout,
			IdleTimeout:       opts.IdleTimeout,
			MaxHeaderBytes:    opts.MaxHeaderBytes,

			// Otherwise net/http internal errors bypass the configured logger.
			ErrorLog: opts.Logger.Std("", 0),
		},
	}
}

// Start serves in the background and reports a fatal error on the returned
// channel. The channel closes with no value when the shutdown was ordered.
func (s *Server) Start() <-chan error {
	failure := make(chan error, 1)

	go func() {
		defer close(failure)

		s.logger.Info().
			Str("address", s.server.Addr).
			Str("scheme", s.scheme()).
			Msg("http server listening")

		if err := s.listen(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failure <- err
		}
	}()

	return failure
}

// Shutdown waits for in-flight requests within the context deadline, then drops
// whatever is left rather than holding the process.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Error().Err(err).Msg("graceful shutdown failed, closing open connections")

		return errors.Join(err, s.server.Close())
	}

	return nil
}

// listen takes no file names: the certificate comes from the TLS configuration,
// which resolves it per handshake and can therefore be rotated under a running
// server.
func (s *Server) listen() error {
	if s.server.TLSConfig == nil {
		return s.server.ListenAndServe()
	}

	return s.server.ListenAndServeTLS("", "")
}

func (s *Server) scheme() string {
	if s.server.TLSConfig == nil {
		return "http"
	}

	return "https"
}
