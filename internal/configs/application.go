package configs

import (
	"errors"
	"fmt"
)

type Application struct {
	// Version, revision and build time are not here on purpose: they identify
	// the binary and live in internal/buildinfo, where the environment cannot
	// contradict them.
	AppName string `yaml:"app_name"`

	Logging    *Logging    `yaml:"logging"`
	HttpServer *HttpServer `yaml:"http_server"`
	Graceful   *Graceful   `yaml:"graceful"`
	Health     *Health     `yaml:"health"`
	Banner     *Banner     `yaml:"banner"`
}

// Validate aggregates every section so a single boot reports all the invalid
// settings at once, instead of one per run.
func (cfg *Application) Validate() error {
	var errs []error

	if cfg.AppName == "" {
		errs = append(errs, errors.New("application: name is empty"))
	}

	// The sections are checked one by one on purpose: putting the pointers in a
	// slice of interfaces would make a nil section compare as non-nil, and the
	// missing-section branch would never run.
	if cfg.Logging == nil {
		errs = append(errs, errors.New("application: logging configuration is missing"))
	} else {
		errs = append(errs, cfg.Logging.Validate())
	}

	if cfg.HttpServer == nil {
		errs = append(errs, errors.New("application: http server configuration is missing"))
	} else {
		errs = append(errs, cfg.HttpServer.Validate())
	}

	if cfg.Graceful == nil {
		errs = append(errs, errors.New("application: graceful configuration is missing"))
	} else {
		errs = append(errs, cfg.Graceful.Validate())
	}

	if cfg.Health == nil {
		errs = append(errs, errors.New("application: health configuration is missing"))
	} else {
		errs = append(errs, cfg.Health.Validate())
	}

	if cfg.Banner == nil {
		errs = append(errs, errors.New("application: banner configuration is missing"))
	} else {
		errs = append(errs, cfg.Banner.Validate())
	}

	// The drain happens inside the shutdown budget, so a delay that does not fit
	// would be cut short and the load balancer would never see the instance
	// leave rotation.
	if cfg.Health != nil && cfg.Graceful != nil && cfg.Health.DrainDelay >= cfg.Graceful.Timeout {
		errs = append(errs, fmt.Errorf(
			"application: health drain delay (%s) must be shorter than the graceful timeout (%s)",
			cfg.Health.DrainDelay, cfg.Graceful.Timeout,
		))
	}

	return errors.Join(errs...)
}
