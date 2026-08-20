package logging_test

import (
	"testing"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/infra/logging"
)

func TestNewParsesEveryLevel(t *testing.T) {
	cases := map[string]log.Level{
		"debug": log.DebugLevel,
		"info":  log.InfoLevel,
		"warn":  log.WarnLevel,
		"error": log.ErrorLevel,
		"fatal": log.FatalLevel,
		"panic": log.PanicLevel,
		// Spelling is not what selects a level.
		"DEBUG": log.DebugLevel,
		"WaRn":  log.WarnLevel,
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := logging.New(logging.Options{Level: input}).Level; got != want {
				t.Errorf("want %v, got %v", want, got)
			}
		})
	}
}

// An unreadable level falls back rather than failing, and info is the only
// choice that neither hides problems nor floods the log. Configuration
// validation is what rejects the value in the first place; this is the last
// resort behind it.
func TestNewFallsBackToInfo(t *testing.T) {
	for _, input := range []string{"", "banana", "trace"} {
		if got := logging.New(logging.Options{Level: input}).Level; got != log.InfoLevel {
			t.Errorf("%q: want info, got %v", input, got)
		}
	}
}

func TestNewKeepsTheTimeFieldOptional(t *testing.T) {
	if got := logging.New(logging.Options{Level: "info"}).TimeField; got != "" {
		t.Errorf("an unset time field should stay at the library default, got %q", got)
	}

	if got := logging.New(logging.Options{Level: "info", TimeField: "ts"}).TimeField; got != "ts" {
		t.Errorf("want ts, got %q", got)
	}
}

func TestSetDefaultPointsThePackageLoggerAtTheInstance(t *testing.T) {
	previous := log.DefaultLogger
	t.Cleanup(func() { log.DefaultLogger = previous })

	logging.SetDefault(logging.New(logging.Options{Level: "error", TimeFormat: "2006"}))

	if log.DefaultLogger.Level != log.ErrorLevel || log.DefaultLogger.TimeFormat != "2006" {
		t.Errorf("the package logger should mirror the instance, got %+v", log.DefaultLogger)
	}
}
