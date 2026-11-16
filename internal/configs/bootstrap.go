package configs

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	bootstrapEmailMaxBytes    = 254
	bootstrapUsernameMaxBytes = 64

	// The limit the credential service enforces, repeated here so an oversized
	// password fails the boot instead of the bootstrap.
	bootstrapPasswordMaxBytes = 1024
)

// Bootstrap is held by value, unlike the other sections: it has no defaults
// and is optional, so a `bootstrap:` key left with no body must not turn into
// a missing section.
type Bootstrap struct {
	Admin BootstrapAdmin `yaml:"admin"`
}

// BootstrapAdmin is the first administrator of the master realm. It is only
// acted on while that realm has no administrator able to sign in.
type BootstrapAdmin struct {
	Email    string `yaml:"email"`
	Username string `yaml:"username"`

	// Out of YAML for the same reason as the database password.
	Password string `yaml:"-"`
}

func (cfg *BootstrapAdmin) Configured() bool {
	return cfg.Email != "" && cfg.Password != ""
}

func (cfg *Bootstrap) Validate() error {
	return cfg.Admin.Validate()
}

// Validate has no strength rule on purpose: the credential is created marked
// for mandatory change, and refusing a weak one would leave the installation
// with no administrator at all.
func (cfg *BootstrapAdmin) Validate() error {
	if cfg.Email == "" && cfg.Username == "" && cfg.Password == "" {
		return nil
	}

	var errs []error

	if cfg.Email == "" {
		errs = append(errs, errors.New(
			"bootstrap: admin email is required: it is what identifies the administrator. "+
				"Set AEGIS_BOOTSTRAP_ADMIN_EMAIL or bootstrap.admin.email",
		))
	} else {
		errs = append(errs, cfg.validateEmail())
	}

	if cfg.Username != "" {
		errs = append(errs, cfg.validateUsername())
	}

	// The value itself is never in a message: it is the secret.
	switch {
	case cfg.Password == "":
		errs = append(errs, errors.New(
			"bootstrap: admin password is required once an email or a username is set: "+
				"set AEGIS_BOOTSTRAP_ADMIN_PASSWORD, or AEGIS_BOOTSTRAP_ADMIN_PASSWORD_FILE pointing at "+
				"a mounted secret. It cannot be read from the configuration file",
		))
	case len(cfg.Password) > bootstrapPasswordMaxBytes:
		errs = append(errs, fmt.Errorf(
			"bootstrap: admin password is %d bytes, and at most %d are accepted",
			len(cfg.Password), bootstrapPasswordMaxBytes,
		))
	}

	return errors.Join(errs...)
}

// validateEmail checks the shape only; the identity domain validates in full
// when the administrator is created.
func (cfg *BootstrapAdmin) validateEmail() error {
	if len(cfg.Email) > bootstrapEmailMaxBytes {
		return fmt.Errorf(
			"bootstrap: admin email is %d bytes, and at most %d are accepted",
			len(cfg.Email), bootstrapEmailMaxBytes,
		)
	}

	local, domain, found := strings.Cut(cfg.Email, "@")
	if !found || local == "" || domain == "" || strings.Contains(domain, "@") ||
		strings.ContainsFunc(cfg.Email, unicode.IsSpace) {
		return fmt.Errorf("bootstrap: admin email %q is not an email address", cfg.Email)
	}

	return nil
}

func (cfg *BootstrapAdmin) validateUsername() error {
	if len(cfg.Username) > bootstrapUsernameMaxBytes {
		return fmt.Errorf(
			"bootstrap: admin username is %d bytes, and at most %d are accepted",
			len(cfg.Username), bootstrapUsernameMaxBytes,
		)
	}

	if strings.Contains(cfg.Username, "@") || strings.ContainsFunc(cfg.Username, unicode.IsSpace) {
		return fmt.Errorf(
			"bootstrap: admin username %q cannot contain %q or whitespace", cfg.Username, "@",
		)
	}

	return nil
}
