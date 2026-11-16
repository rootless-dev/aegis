package session

import "errors"

var (
	ErrNotFound         = errors.New("session: not found")
	ErrIDInvalid        = errors.New("session: identifier is empty")
	ErrRealmInvalid     = errors.New("session: realm is empty")
	ErrIdentityInvalid  = errors.New("session: identity is empty")
	ErrSessionInvalid   = errors.New("session: refresh token has no session")
	ErrTTLInvalid       = errors.New("session: expiry is not after creation")
	ErrLastSeenInvalid  = errors.New("session: last seen is before creation")
	ErrIPInvalid        = errors.New("session: ip is not an address")
	ErrUserAgentInvalid = errors.New("session: user agent is too long")
	ErrEndInvalid       = errors.New("session: end time and end reason must be set together")
	ErrEndReasonInvalid = errors.New("session: end reason is not one of revoked, reuse, realm_disabled")
	ErrEnded            = errors.New("session: ended")
	ErrExpired          = errors.New("session: expired")
	ErrHashInvalid      = errors.New("session: refresh token hash is not well formed")
	ErrTokenNotFound    = errors.New("session: refresh token not found")
	ErrTokenUsed        = errors.New("session: refresh token already used")
	ErrTokenExpired     = errors.New("session: refresh token expired")

	// Raised by the service: a consumed token presented again is the signal of
	// theft, and the answer is ending the session.
	ErrReuseDetected = errors.New("session: refresh token reuse detected")
)
