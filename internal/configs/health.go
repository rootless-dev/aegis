package configs

import (
	"errors"
	"fmt"
	"time"
)

type Health struct {
	// CheckTimeout bounds each readiness check. A probe that hangs is worse
	// than one that fails, since the orchestrator waits for its own timeout.
	CheckTimeout time.Duration `yaml:"check_timeout"`

	// DrainDelay is how long readiness keeps failing before the server stops
	// accepting connections, so the load balancer notices first.
	DrainDelay time.Duration `yaml:"drain_delay"`
}

func defaultHealth() *Health {
	return &Health{
		CheckTimeout: 2 * time.Second,
		DrainDelay:   5 * time.Second,
	}
}

func (cfg *Health) Validate() error {
	var errs []error

	if cfg.CheckTimeout <= 0 {
		errs = append(errs, fmt.Errorf("health: check timeout must be greater than zero, got %s", cfg.CheckTimeout))
	}

	if cfg.DrainDelay < 0 {
		errs = append(errs, fmt.Errorf("health: drain delay must not be negative, got %s", cfg.DrainDelay))
	}

	return errors.Join(errs...)
}
