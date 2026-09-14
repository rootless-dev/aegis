package configs

import (
	"encoding/base64"
	"errors"
	"fmt"
)

// AES-256, so the key is 32 bytes and nothing else is accepted.
const masterKeyBytes = 32

// Crypto carries the secret that encrypts every realm's private signing key.
type Crypto struct {
	MasterKey string `yaml:"-"`

	// Set only while a rewrap is in flight: the sealer opens each row with
	// whichever key matches its recorded kek id.
	MasterKeyPrevious string `yaml:"-"`
}

// No default value, and no development fallback. A constant shipped in the
// binary would be a decryption key every installation shares, and safety would
// rest on a guard rather than on the secret not existing. `make master-key`
// generates one into .env instead: the tooling supplies it, not the product.
func defaultCrypto() *Crypto {
	return &Crypto{}
}

// Validate takes no profile: the rule is the same in both, which is the point
// of having no fallback.
func (cfg *Crypto) Validate() error {
	var errs []error

	if cfg.MasterKey == "" {
		errs = append(errs, errors.New(
			"crypto: master key is required: it encrypts every realm's signing key, and no realm can sign "+
				"without it. Run `make master-key` for development, or generate one with "+
				"`openssl rand -base64 32` and set AEGIS_CRYPTO_MASTER_KEY, or AEGIS_CRYPTO_MASTER_KEY_FILE pointing at "+
				"a mounted secret",
		))
	}

	errs = append(errs, validateMasterKey("master key", cfg.MasterKey))
	errs = append(errs, validateMasterKey("previous master key", cfg.MasterKeyPrevious))

	return errors.Join(errs...)
}

// Bytes decodes the master key; an error here means Validate never ran.
func (cfg *Crypto) Bytes() ([]byte, error) {
	return decodeMasterKey("master key", cfg.MasterKey)
}

// PreviousBytes returns nil, nil when none is configured: only a rewrap sets one.
func (cfg *Crypto) PreviousBytes() ([]byte, error) {
	if cfg.MasterKeyPrevious == "" {
		return nil, nil
	}

	return decodeMasterKey("previous master key", cfg.MasterKeyPrevious)
}

func validateMasterKey(name, raw string) error {
	if raw == "" {
		return nil
	}

	_, err := decodeMasterKey(name, raw)

	return err
}

func decodeMasterKey(name, raw string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// The value itself is never in the message: it is the secret.
		return nil, fmt.Errorf("crypto: %s is not valid base64: %w", name, err)
	}

	if len(decoded) != masterKeyBytes {
		return nil, fmt.Errorf(
			"crypto: %s decodes to %d bytes and has to be exactly %d. Generate one with `openssl rand -base64 32`",
			name, len(decoded), masterKeyBytes,
		)
	}

	return decoded, nil
}
