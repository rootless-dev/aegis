package session

import "fmt"

// EndReason is empty while the session is live.
type EndReason string

const (
	// An operator or the identity ended it.
	EndRevoked EndReason = "revoked"

	// A consumed refresh token was presented again.
	EndReuse EndReason = "reuse"

	EndRealmDisabled EndReason = "realm_disabled"
)

func ParseEndReason(raw string) (EndReason, error) {
	switch reason := EndReason(raw); reason {
	case EndRevoked, EndReuse, EndRealmDisabled:
		return reason, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrEndReasonInvalid, raw)
	}
}

func (r EndReason) String() string { return string(r) }
