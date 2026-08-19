package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/phuslu/log"
)

type Options struct {
	Address string
	Handler http.Handler
	Logger  *log.Logger

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

		s.logger.Info().Str("address", s.server.Addr).Msg("http server listening")

		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
