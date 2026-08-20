package configs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HSTS tells browsers to refuse plain HTTP for this host. It is only ever sent
// over a connection that already is HTTPS: announcing it over HTTP asks the
// browser to trust the very message an attacker could have written.
type HSTS struct {
	Enabled bool `yaml:"enabled"`

	MaxAge time.Duration `yaml:"max_age"`

	// IncludeSubdomains reaches every host under this domain, including ones
	// this deployment knows nothing about, which is why it stays off by default.
	IncludeSubdomains bool `yaml:"include_subdomains"`
}

func defaultHSTS() *HSTS {
	return &HSTS{
		Enabled: true,
		MaxAge:  365 * 24 * time.Hour,
		// Off: it would reach hosts this deployment knows nothing about, and a
		// wrong one cannot be taken back until the announced max age runs out on
		// each browser.
		IncludeSubdomains: false,
	}
}

func (cfg *HSTS) Validate() error {
	if !cfg.Enabled {
		return nil
	}

	if cfg.MaxAge <= 0 {
		return fmt.Errorf("hsts: max age must be greater than zero when enabled, got %s", cfg.MaxAge)
	}

	if cfg.MaxAge < time.Second {
		return errors.New("hsts: max age is sent in whole seconds, so anything below one second would be sent as zero")
	}

	return nil
}

func (cfg *HSTS) HeaderValue() string {
	var builder strings.Builder

	builder.WriteString("max-age=")
	builder.WriteString(strconv.FormatInt(int64(cfg.MaxAge.Seconds()), 10))

	if cfg.IncludeSubdomains {
		builder.WriteString("; includeSubDomains")
	}

	return builder.String()
}
