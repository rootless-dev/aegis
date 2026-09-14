package key

import "errors"

var (
	ErrNotFound = errors.New("key: not found")

	// Separate from ErrNotFound because the callers differ: SignerFor turning up
	// empty is a realm that cannot sign, Rotate turning up empty is ordinary.
	ErrNoActiveKey = errors.New("key: the realm has no active key for that algorithm")

	// uq_realm_keys_active firing.
	ErrActiveKeyExists = errors.New("key: the realm already has an active key for that algorithm")

	// uq_realm_keys_kid firing.
	ErrKIDTaken = errors.New("key: the realm already has a key with that kid")

	ErrUnsupportedAlgorithm = errors.New("key: unsupported algorithm")

	ErrInvalidTransition = errors.New("key: invalid status transition")
)
