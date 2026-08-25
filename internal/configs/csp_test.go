package configs_test

import (
	"testing"

	"github.com/rootless-dev/aegis/internal/configs"
)

func TestCSPDefaultsToEnabled(t *testing.T) {
	cfg := configs.Default()

	if cfg.CSP == nil {
		t.Fatal("csp section is missing from the defaults")
	}

	if !cfg.CSP.Enabled {
		t.Error("csp must default to enabled, including under the prod profile")
	}
}

func TestCSPValidateAcceptsBothStates(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := &configs.CSP{Enabled: enabled}

		if err := cfg.Validate(); err != nil {
			t.Errorf("enabled=%v: unexpected error: %v", enabled, err)
		}
	}
}
