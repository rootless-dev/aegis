package configs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/configs"
)

func TestMigrateDefaults(t *testing.T) {
	cfg := configs.Default()

	if cfg.Database == nil || cfg.Database.Migrate == nil {
		t.Fatal("the migrate section is missing from the defaults")
	}

	// On by default: the reference installation is a single on-prem process,
	// where a two-step start would have to be read about to work at all.
	if !cfg.Database.Migrate.OnBoot {
		t.Error("on_boot must default to true")
	}

	if cfg.Database.Migrate.Timeout != 5*time.Minute {
		t.Errorf("timeout: want 5m, got %s", cfg.Database.Migrate.Timeout)
	}

	// Zero means "leave golang-migrate's own default alone", which is a
	// deliberate value and not an unset one.
	if cfg.Database.Migrate.LockTimeout != 0 {
		t.Errorf("lock timeout: want 0, got %s", cfg.Database.Migrate.LockTimeout)
	}
}

func TestMigrateValidateRejectsANonPositiveTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		cfg := &configs.Migrate{OnBoot: true, Timeout: timeout}

		err := cfg.Validate()
		if err == nil {
			t.Fatalf("timeout %s must be refused", timeout)
		}

		if !strings.Contains(err.Error(), "migrate timeout") {
			t.Errorf("the error should name the setting, got: %v", err)
		}
	}
}

func TestMigrateValidateRejectsANegativeLockTimeout(t *testing.T) {
	cfg := &configs.Migrate{OnBoot: true, Timeout: time.Minute, LockTimeout: -time.Second}

	if err := cfg.Validate(); err == nil {
		t.Fatal("a negative lock timeout must be refused")
	}
}

// The timeout is validated even with migration disabled: a configuration that
// only becomes invalid when a flag flips is a trap for whoever flips it.
func TestMigrateValidateChecksTheTimeoutEvenWhenDisabled(t *testing.T) {
	cfg := &configs.Migrate{OnBoot: false, Timeout: 0}

	if err := cfg.Validate(); err == nil {
		t.Fatal("the timeout must be validated regardless of on_boot")
	}
}

func TestDatabaseValidateReportsAMissingMigrateSection(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.Normalize()
	cfg.Database.Migrate = nil

	err := cfg.Database.Validate(configs.ProfileDev)
	if err == nil {
		t.Fatal("a missing migrate section must be reported")
	}

	if !strings.Contains(err.Error(), "migrate configuration is missing") {
		t.Errorf("want a missing-section error, got: %v", err)
	}
}
