package credential

import "fmt"

type Type string

const TypePassword Type = "password"

func ParseType(raw string) (Type, error) {
	if Type(raw) == TypePassword {
		return TypePassword, nil
	}

	return "", fmt.Errorf("%w: %q", ErrTypeInvalid, raw)
}

func (t Type) String() string { return string(t) }
