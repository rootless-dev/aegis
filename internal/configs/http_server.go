package configs

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

type HttpServer struct {
	Port string `yaml:"port"`
	Host string `yaml:"host"`

	// ReadHeaderTimeout is the slowloris defense: it bounds connections that
	// trickle headers byte by byte to hold a worker.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`

	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	IdleTimeout  time.Duration `yaml:"idle_timeout"`

	// RequestTimeout is carried on the request context. It does not interrupt
	// the handler, only what honors the context.
	RequestTimeout time.Duration `yaml:"request_timeout"`

	MaxHeaderBytes int `yaml:"max_header_bytes"`
}

func defaultHttpServer() *HttpServer {
	return &HttpServer{
		Host:              "0.0.0.0",
		Port:              "7500",
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Has to accommodate the worst case of the authentication path, argon2id
		// included.
		WriteTimeout:   15 * time.Second,
		IdleTimeout:    60 * time.Second,
		RequestTimeout: 10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
}

func (cfg *HttpServer) Validate() error {
	var errs []error

	if cfg.Host == "" {
		errs = append(errs, errors.New("http server: host is empty"))
	}

	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		errs = append(errs, fmt.Errorf("http server: invalid port %q", cfg.Port))
	}

	durations := map[string]time.Duration{
		"read header timeout": cfg.ReadHeaderTimeout,
		"read timeout":        cfg.ReadTimeout,
		"write timeout":       cfg.WriteTimeout,
		"idle timeout":        cfg.IdleTimeout,
		"request timeout":     cfg.RequestTimeout,
	}

	for name, d := range durations {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("http server: %s must be greater than zero, got %s", name, d))
		}
	}

	if cfg.ReadHeaderTimeout > cfg.ReadTimeout {
		errs = append(errs, fmt.Errorf(
			"http server: read header timeout (%s) must not exceed read timeout (%s)",
			cfg.ReadHeaderTimeout, cfg.ReadTimeout,
		))
	}

	// Otherwise the connection dies before the handler can answer the timeout.
	if cfg.RequestTimeout > cfg.WriteTimeout {
		errs = append(errs, fmt.Errorf(
			"http server: request timeout (%s) must not exceed write timeout (%s)",
			cfg.RequestTimeout, cfg.WriteTimeout,
		))
	}

	if cfg.MaxHeaderBytes <= 0 {
		errs = append(errs, fmt.Errorf("http server: max header bytes must be greater than zero, got %d", cfg.MaxHeaderBytes))
	}

	return errors.Join(errs...)
}

func (cfg *HttpServer) Address() string {
	return net.JoinHostPort(cfg.Host, cfg.Port)
}
