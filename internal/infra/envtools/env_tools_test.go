package envtools_test

import (
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/infra/envtools"
)

func TestGetEnvParsesEverySupportedType(t *testing.T) {
	t.Setenv("AEGIS_STRING", "value")
	t.Setenv("AEGIS_INT", "42")
	t.Setenv("AEGIS_INT8", "7")
	t.Setenv("AEGIS_BOOL", "true")
	t.Setenv("AEGIS_FLOAT32", "1.5")
	t.Setenv("AEGIS_FLOAT64", "2.5")

	if got := envtools.GetEnv("AEGIS_STRING", "fallback"); got != "value" {
		t.Errorf("string: want %q, got %q", "value", got)
	}

	if got := envtools.GetEnv("AEGIS_INT", 0); got != 42 {
		t.Errorf("int: want 42, got %d", got)
	}

	if got := envtools.GetEnv[int8]("AEGIS_INT8", 1); got != 7 {
		t.Errorf("int8: want 7, got %d", got)
	}

	if got := envtools.GetEnv("AEGIS_BOOL", false); !got {
		t.Errorf("bool: want true, got %v", got)
	}

	if got := envtools.GetEnv[float32]("AEGIS_FLOAT32", 0); got != 1.5 {
		t.Errorf("float32: want 1.5, got %v", got)
	}

	if got := envtools.GetEnv("AEGIS_FLOAT64", 0.0); got != 2.5 {
		t.Errorf("float64: want 2.5, got %v", got)
	}
}

func TestGetEnvFallsBackOnAbsentOrInvalidValues(t *testing.T) {
	t.Setenv("AEGIS_EMPTY", "")
	t.Setenv("AEGIS_NOT_A_NUMBER", "banana")
	t.Setenv("AEGIS_OVERFLOWS_INT8", "300")

	if got := envtools.GetEnv("AEGIS_MISSING", "fallback"); got != "fallback" {
		t.Errorf("absent: want %q, got %q", "fallback", got)
	}

	if got := envtools.GetEnv("AEGIS_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("empty: want %q, got %q", "fallback", got)
	}

	if got := envtools.GetEnv("AEGIS_NOT_A_NUMBER", 9); got != 9 {
		t.Errorf("unparsable: want 9, got %d", got)
	}

	if got := envtools.GetEnv[int8]("AEGIS_OVERFLOWS_INT8", 9); got != 9 {
		t.Errorf("out of range: want 9, got %d", got)
	}
}

func TestLookupDistinguishesUnsetFromZero(t *testing.T) {
	t.Setenv("AEGIS_ZERO_INT", "0")
	t.Setenv("AEGIS_FALSE_BOOL", "false")
	t.Setenv("AEGIS_BLANK", "")

	// A zero value that was deliberately set must not look like an absent one,
	// or a later layer could never turn a default off.
	if value, ok := envtools.Lookup[int]("AEGIS_ZERO_INT"); !ok || value != 0 {
		t.Errorf("explicit 0 should be reported as set, got (%v, %v)", value, ok)
	}

	if value, ok := envtools.Lookup[bool]("AEGIS_FALSE_BOOL"); !ok || value {
		t.Errorf("explicit false should be reported as set, got (%v, %v)", value, ok)
	}

	if _, ok := envtools.Lookup[string]("AEGIS_ABSENT"); ok {
		t.Error("an absent variable must not be reported as set")
	}

	if _, ok := envtools.Lookup[string]("AEGIS_BLANK"); ok {
		t.Error("an empty variable counts as absent")
	}
}

func TestLookupRejectsUnparsableValues(t *testing.T) {
	t.Setenv("AEGIS_BAD_INT", "banana")

	if _, ok := envtools.Lookup[int]("AEGIS_BAD_INT"); ok {
		t.Error("an unparsable value must not be reported as set")
	}
}

func TestLookupDuration(t *testing.T) {
	t.Setenv("AEGIS_TIMEOUT", "1m30s")
	t.Setenv("AEGIS_BAD_TIMEOUT", "15")

	if value, ok := envtools.LookupDuration("AEGIS_TIMEOUT"); !ok || value != 90*time.Second {
		t.Errorf("want 1m30s, got (%v, %v)", value, ok)
	}

	// A bare number is not a duration: accepting it would silently mean
	// nanoseconds.
	if _, ok := envtools.LookupDuration("AEGIS_BAD_TIMEOUT"); ok {
		t.Error("a unitless value must be rejected")
	}
}
