package admin

import "errors"

var (
	ErrNotFound        = errors.New("admin: grant not found")
	ErrIDInvalid       = errors.New("admin: identifier is empty")
	ErrRealmInvalid    = errors.New("admin: realm is empty")
	ErrIdentityInvalid = errors.New("admin: identity is empty")
	ErrTargetInvalid   = errors.New("admin: target realm is empty")

	// Raised by the repository, from the two unique indexes.
	ErrExists = errors.New("admin: the identity already holds that grant")

	// Raised by the service.
	ErrNotAuthorized = errors.New("admin: not authorized")
)
