package identity

import "fmt"

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

func ParseStatus(raw string) (Status, error) {
	switch status := Status(raw); status {
	case StatusActive, StatusDisabled:
		return status, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrStatusInvalid, raw)
	}
}

func (s Status) String() string { return string(s) }
