package configs

import (
	"errors"
	"fmt"
	"time"
)

// Migrate configures how the schema reaches the version the binary carries.
type Migrate struct {
	// Turning this off does not turn off the version check: the boot still
	// refuses a schema older than the binary.
	OnBoot bool `yaml:"on_boot"`

	// Bounds how long new migrations keep being started, not how long the boot
	// takes: one already running is allowed to finish.
	Timeout time.Duration `yaml:"timeout"`

	// Zero is not "wait indefinitely" — it leaves golang-migrate's own default
	// of 15 seconds.
	LockTimeout time.Duration `yaml:"lock_timeout"`
}

func defaultMigrate() *Migrate {
	return &Migrate{
		OnBoot:  true,
		Timeout: 5 * time.Minute,
	}
}

func (cfg *Migrate) Validate() error {
	var errs []error

	// Checked even when OnBoot is false, so the failure lands on whoever wrote
	// the value rather than on whoever later flips the flag.
	if cfg.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("database: migrate timeout must be greater than zero, got %s", cfg.Timeout))
	}

	if cfg.LockTimeout < 0 {
		errs = append(errs, fmt.Errorf("database: migrate lock timeout cannot be negative, got %s", cfg.LockTimeout))
	}

	return errors.Join(errs...)
}
