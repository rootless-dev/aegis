package key

import "fmt"

// Purpose is the JWK "use" value, and it is part of uq_realm_keys_active.
type Purpose string

const PurposeSignature Purpose = "sig"

func (p Purpose) String() string { return string(p) }

func ParsePurpose(raw string) (Purpose, error) {
	if Purpose(raw) == PurposeSignature {
		return PurposeSignature, nil
	}

	return "", fmt.Errorf("key: %q is not a supported purpose", raw)
}
