package logging

import (
	"os"
	"strings"

	"github.com/phuslu/log"
)

type Options struct {
	Level         string
	Caller        int
	TimeField     string
	TimeFormat    string
	PrettyEnabled bool
}

func New(opts Options) *log.Logger {
	instance := log.Logger{
		Level:      parseLevel(opts.Level),
		Caller:     opts.Caller,
		TimeFormat: opts.TimeFormat,
	}

	if opts.TimeField != "" {
		instance.TimeField = opts.TimeField
	}

	// The console writer is only useful on a terminal; under an orchestrator the
	// structured output is what gets collected.
	if opts.PrettyEnabled && log.IsTerminal(os.Stderr.Fd()) {
		instance.Writer = &log.ConsoleWriter{
			ColorOutput:    true,
			QuoteString:    true,
			EndWithMessage: true,
		}
	}

	return &instance
}

// SetDefault points the package level logger at instance. It exists as its own
// call so that the global mutation is visible at the call site instead of
// hidden inside the constructor.
func SetDefault(instance *log.Logger) {
	log.DefaultLogger = *instance
}

func parseLevel(level string) log.Level {
	switch strings.ToLower(level) {
	case "debug":
		return log.DebugLevel
	case "info":
		return log.InfoLevel
	case "warn":
		return log.WarnLevel
	case "error":
		return log.ErrorLevel
	case "fatal":
		return log.FatalLevel
	case "panic":
		return log.PanicLevel
	default:
		return log.InfoLevel
	}
}
