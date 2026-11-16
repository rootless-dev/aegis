package realm

import (
	"fmt"
	"time"
)

// Ten years: far past any real lifetime, and the seconds still fit a 32-bit
// integer column.
const maxTTL = 10 * 365 * 24 * time.Hour

// Settings is what a realm decides about the tokens and sessions it issues.
type Settings struct {
	AccessTokenTTL time.Duration
	SessionIdleTTL time.Duration
	SessionMaxTTL  time.Duration

	// PasswordGrant allows the direct password grant at the token endpoint.
	// Phase 1 has no clients and no authorization code flow, so it is the only
	// way in: on in master, where the administration lives, off elsewhere.
	PasswordGrant bool
}

func DefaultSettings() Settings {
	return Settings{
		AccessTokenTTL: 5 * time.Minute,
		SessionIdleTTL: 30 * time.Minute,
		SessionMaxTTL:  10 * time.Hour,
	}
}

func (s Settings) Validate() error {
	for _, ttl := range []struct {
		name  string
		value time.Duration
	}{
		{"access token ttl", s.AccessTokenTTL},
		{"session idle ttl", s.SessionIdleTTL},
		{"session max ttl", s.SessionMaxTTL},
	} {
		// The columns hold whole seconds, so a fraction would not survive a save.
		if ttl.value <= 0 || ttl.value > maxTTL || ttl.value%time.Second != 0 {
			return fmt.Errorf(
				"%w: %s is %s, want whole seconds between 1s and %s",
				ErrSettingsInvalid, ttl.name, ttl.value, maxTTL,
			)
		}
	}

	if s.SessionIdleTTL > s.SessionMaxTTL {
		return fmt.Errorf(
			"%w: session idle ttl %s exceeds session max ttl %s",
			ErrSettingsInvalid, s.SessionIdleTTL, s.SessionMaxTTL,
		)
	}

	if s.AccessTokenTTL > s.SessionMaxTTL {
		return fmt.Errorf(
			"%w: access token ttl %s exceeds session max ttl %s",
			ErrSettingsInvalid, s.AccessTokenTTL, s.SessionMaxTTL,
		)
	}

	return nil
}
