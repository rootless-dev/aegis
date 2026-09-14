package envtools

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/phuslu/log"
)

type AllowedEnvTypes interface {
	int | int8 | string | bool | float32 | float64
}

// GetEnv reads key and falls back to defaultValue when the variable is absent,
// empty, or cannot be parsed into T.
func GetEnv[T AllowedEnvTypes](key string, defaultValue T) T {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return defaultValue
	}

	parsed, ok := parse[T](key, raw)
	if !ok {
		return defaultValue
	}

	return parsed
}

// Lookup reads key without supplying a default, reporting whether it was set.
// This is what lets configuration sources be layered: a source must only
// overwrite what it actually declares, otherwise a default would beat a value
// deliberately set by an earlier layer.
func Lookup[T AllowedEnvTypes](key string) (T, bool) {
	var zero T

	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return zero, false
	}

	return parse[T](key, raw)
}

// LookupSecret reads a secret from key, or from the file whose path key_FILE
// names, which is the shape a mounted Docker or Kubernetes secret takes. Both
// forms set is an error rather than a precedence rule: a silent winner is how
// the wrong secret gets used for months.
func LookupSecret(key string) (string, bool, error) {
	direct, hasDirect := Lookup[string](key)
	path, hasFile := Lookup[string](key + "_FILE")

	switch {
	case hasDirect && hasFile:
		return "", false, fmt.Errorf("envtools: %s and %s_FILE are both set, and only one may be", key, key)
	case hasFile:
		content, err := os.ReadFile(path)
		if err != nil {
			return "", false, fmt.Errorf("envtools: reading %s_FILE at %q: %w", key, path, err)
		}

		// `echo` and editors leave a trailing newline, and a secret is compared
		// byte for byte.
		return strings.TrimSpace(string(content)), true, nil
	case hasDirect:
		return direct, true, nil
	}

	return "", false, nil
}

// LookupDuration reads a Go duration such as "15s" or "1m30s".
func LookupDuration(key string) (time.Duration, bool) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return 0, false
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		log.Error().Err(err).
			Str("key", key).
			Str("value", raw).
			Msg("environment variable is not a valid duration, ignoring it")

		return 0, false
	}

	return parsed, true
}

// parse converts raw into T, reporting failure rather than a value. Every
// failure is logged: a silently ignored setting is indistinguishable from one
// that was never written.
func parse[T AllowedEnvTypes](key, raw string) (T, bool) {
	var zero T

	var data any

	var err error

	switch any(zero).(type) {
	case float32:
		var parsed float64
		parsed, err = strconv.ParseFloat(raw, 32)
		data = float32(parsed)
	case float64:
		data, err = strconv.ParseFloat(raw, 64)
	case bool:
		data, err = strconv.ParseBool(raw)
	case string:
		data = raw
	case int:
		data, err = strconv.Atoi(raw)
	case int8:
		// ParseInt bounds the value but still returns int64, so the conversion
		// is what makes the assertion below succeed.
		var parsed int64
		parsed, err = strconv.ParseInt(raw, 10, 8)
		data = int8(parsed)
	default:
		log.Error().
			Str("key", key).
			Str("value", raw).
			Msg("environment variable type is not supported, ignoring it")

		return zero, false
	}

	if err != nil {
		log.Error().Err(err).
			Str("key", key).
			Str("value", raw).
			Msg("environment variable could not be parsed, ignoring it")

		return zero, false
	}

	result, ok := data.(T)
	if !ok {
		log.Error().
			Str("key", key).
			Str("value", raw).
			Msg("parsed environment variable does not match the expected type, ignoring it")

		return zero, false
	}

	return result, true
}
