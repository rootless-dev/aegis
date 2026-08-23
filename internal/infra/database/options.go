package database

import (
	"time"

	"github.com/phuslu/log"
)

// Options mirrors no configuration struct on purpose, so this package can be
// used without assembling the application around it.
type Options struct {
	Driver Driver

	Host     string
	Port     string
	Name     string
	User     string
	Password string

	Path string

	// SSLMode is the shared vocabulary each dialect translates: disable,
	// prefer, require, verify-ca or verify-full.
	SSLMode     string
	SSLRootCert string

	Options map[string]string

	ConnectTimeout time.Duration

	Pool Pool

	Logger *log.Logger

	// SlowThreshold is how long a query may take before it is reported. Zero
	// disables the report.
	SlowThreshold time.Duration

	// LogParameters renders query arguments into the log. Off outside
	// development: they are credentials, tokens and personal data.
	LogParameters bool
}

// logger falls back so a caller that left Logger unset does not take a nil
// dereference on its first statement.
func (o Options) logger() *log.Logger {
	if o.Logger == nil {
		return &log.DefaultLogger
	}

	return o.Logger
}

type Pool struct {
	MaxOpen         int
	MaxIdle         int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}
