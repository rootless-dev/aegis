package realm

import "fmt"

// Status is a closed set, mirrored by a named CHECK constraint in all four
// dialects.
type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"

	// An archived realm keeps occupying its slug and issuer: handing either to
	// a new realm would point cached discovery and live tokens at it.
	StatusArchived Status = "archived"
)

func ParseStatus(raw string) (Status, error) {
	switch status := Status(raw); status {
	case StatusActive, StatusDisabled, StatusArchived:
		return status, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrStatusInvalid, raw)
	}
}

func (s Status) String() string { return string(s) }
