package key

import (
	"fmt"
	"slices"
)

type Status string

const (
	// Signs new tokens, and is published.
	StatusActive Status = "active"
	// Signs nothing, still published: tokens it signed live until they expire.
	StatusPassive Status = "passive"
	// Out of the JWKS.
	StatusDisabled Status = "disabled"
)

func (s Status) String() string { return string(s) }

// publishedStatuses is the only place the JWKS rule is written down. Both the
// predicate and the query form below read from it, so a fourth status cannot
// be added to one and forgotten in the other.
var publishedStatuses = []Status{StatusActive, StatusPassive}

// PublishedStatuses is the rule in the shape a repository query takes. The copy
// is what keeps a caller from editing the rule.
func PublishedStatuses() []Status { return slices.Clone(publishedStatuses) }

func (s Status) Published() bool { return slices.Contains(publishedStatuses, s) }

func ParseStatus(raw string) (Status, error) {
	switch Status(raw) {
	case StatusActive:
		return StatusActive, nil
	case StatusPassive:
		return StatusPassive, nil
	case StatusDisabled:
		return StatusDisabled, nil
	}

	return "", fmt.Errorf("key: %q is not a known status", raw)
}
