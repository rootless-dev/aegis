package identity

import "errors"

// ErrNotFound, ErrEmailTaken and ErrUsernameTaken are raised by the
// repository, which is the only place that can see the other rows.
var (
	ErrNotFound        = errors.New("identity: not found")
	ErrIDInvalid       = errors.New("identity: identifier is empty")
	ErrRealmInvalid    = errors.New("identity: realm identifier is empty")
	ErrEmailInvalid    = errors.New("identity: email is not well formed")
	ErrEmailTaken      = errors.New("identity: email is already in use")
	ErrUsernameInvalid = errors.New("identity: username is not well formed")
	ErrUsernameTaken   = errors.New("identity: username is already in use")
	ErrStatusInvalid   = errors.New("identity: status is not one of active, disabled")
)

// ErrDisabled is an identity that exists and may not authenticate. Raised by
// the service.
var ErrDisabled = errors.New("identity: disabled")
