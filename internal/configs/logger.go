package configs

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var supportedLogLevels = []string{"debug", "info", "warn", "error", "fatal", "panic"}

type Logging struct {
	Caller        int    `yaml:"caller"`
	Level         string `yaml:"level"`
	TimeField     string `yaml:"time_field"`
	TimeFormat    string `yaml:"time_format"`
	PrettyEnabled bool   `yaml:"pretty_enabled"`
}

func defaultLogging() *Logging {
	return &Logging{
		Level:         "INFO",
		Caller:        1,
		TimeFormat:    "2006-01-02 15:04:05",
		PrettyEnabled: true,
	}
}

func (cfg *Logging) Validate() error {
	var errs []error

	// Without this an unknown level silently degrades to info, so asking for
	// debug in production would look like it worked.
	if !slices.Contains(supportedLogLevels, strings.ToLower(cfg.Level)) {
		errs = append(errs, fmt.Errorf("logging: unsupported level %q, expected one of %v", cfg.Level, supportedLogLevels))
	}

	if cfg.Caller < 0 {
		errs = append(errs, fmt.Errorf("logging: caller must not be negative, got %d", cfg.Caller))
	}

	if cfg.TimeFormat == "" {
		errs = append(errs, errors.New("logging: time format is empty"))
	}

	return errors.Join(errs...)
}
