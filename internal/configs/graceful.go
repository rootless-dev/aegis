package configs

import (
	"fmt"
	"time"
)

type Graceful struct {
	// Timeout is the whole shutdown budget, shared by every registered pending.
	// It must fit the window the orchestrator gives between SIGTERM and SIGKILL.
	Timeout time.Duration `yaml:"timeout"`
}

func defaultGraceful() *Graceful {
	return &Graceful{
		Timeout: 20 * time.Second,
	}
}

func (cfg *Graceful) Validate() error {
	if cfg.Timeout <= 0 {
		return fmt.Errorf("graceful: timeout must be greater than zero, got %s", cfg.Timeout)
	}

	return nil
}
