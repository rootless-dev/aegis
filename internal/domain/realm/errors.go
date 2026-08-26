package realm

import "errors"

// ErrSlugReserved is raised by the service, not here: the seed creates a realm
// whose slug is reserved and the aggregate has to allow it.
var (
	ErrNotFound           = errors.New("realm: not found")
	ErrSlugInvalid        = errors.New("realm: slug is not well formed")
	ErrSlugReserved       = errors.New("realm: slug is reserved")
	ErrSlugTaken          = errors.New("realm: slug is already in use")
	ErrIssuerInvalid      = errors.New("realm: issuer is not well formed")
	ErrIssuerTaken        = errors.New("realm: issuer is already in use")
	ErrDisplayNameInvalid = errors.New("realm: display name is empty")
	ErrStatusInvalid      = errors.New("realm: status is not one of active, disabled, archived")
	ErrIDInvalid          = errors.New("realm: identifier is empty")
)
