package credential

import "errors"

var (
	ErrNotFound        = errors.New("credential: not found")
	ErrIDInvalid       = errors.New("credential: identifier is empty")
	ErrRealmInvalid    = errors.New("credential: realm is empty")
	ErrIdentityInvalid = errors.New("credential: identity is empty")
	ErrTypeInvalid     = errors.New("credential: type is not one of password")
	ErrHashInvalid     = errors.New("credential: hash is not well formed")

	// Raised by the repository, from the unique index on identity and type.
	ErrExists = errors.New("credential: the identity already has a credential of that type")

	// Both raised by the service: the aggregate never sees a plaintext.
	ErrMismatch        = errors.New("credential: the secret does not match")
	ErrPasswordInvalid = errors.New("credential: password is not acceptable")
)
