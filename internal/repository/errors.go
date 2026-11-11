package repository

import (
	"errors"
	"strings"

	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"gorm.io/gorm"
)

// translate keeps gorm.ErrRecordNotFound and every driver's unique-violation
// text inside this package.
func translate(err error, notFound error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound
	}

	// Keyed off the constraint name: no engine exposes the violation as a typed
	// value the four share.
	message := strings.ToLower(err.Error())

	if taken := violatedConstraint(message); taken != nil {
		return taken
	}

	// SQLite names the columns, not the constraint. Anchored on the prefix and
	// not the column alone: NOT NULL and CHECK violations name it too. The
	// composite indexes lead with realm_id or identity_id, so the distinctive
	// column tells them apart.
	if strings.Contains(message, "unique constraint failed: ") {
		switch {
		case strings.Contains(message, "realms.slug"):
			return realm.ErrSlugTaken
		case strings.Contains(message, "realms.issuer"):
			return realm.ErrIssuerTaken
		case strings.Contains(message, "realm_keys.active_marker"):
			return key.ErrActiveKeyExists
		case strings.Contains(message, "realm_keys.kid"):
			return key.ErrKIDTaken
		case strings.Contains(message, "identities.email"):
			return identity.ErrEmailTaken
		case strings.Contains(message, "identities.username"):
			return identity.ErrUsernameTaken
		case strings.Contains(message, "credentials.type"):
			return credential.ErrExists
		}
	}

	return err
}

var uniqueConstraints = []struct {
	name  string
	taken error
}{
	{"uq_realms_slug", realm.ErrSlugTaken},
	{"uq_realms_issuer", realm.ErrIssuerTaken},
	{"uq_realm_keys_kid", key.ErrKIDTaken},
	{"uq_realm_keys_active", key.ErrActiveKeyExists},
	{"uq_identities_email", identity.ErrEmailTaken},
	{"uq_identities_username", identity.ErrUsernameTaken},
	{"uq_credentials_type", credential.ErrExists},
}

// The last name in the message wins: MySQL and MariaDB quote the duplicate
// value ahead of the key, and a username may legally spell a constraint name.
func violatedConstraint(message string) error {
	var taken error

	last := -1

	for _, constraint := range uniqueConstraints {
		if at := strings.LastIndex(message, constraint.name); at > last {
			last, taken = at, constraint.taken
		}
	}

	return taken
}

// applied is what every single-row write returns through. A write that matched
// no row reports the not-found error rather than success: without it, updating
// a row something else deleted looks like it worked.
func applied(result *gorm.DB, notFound error) error {
	if result.Error != nil {
		return translate(result.Error, notFound)
	}

	if result.RowsAffected == 0 {
		return notFound
	}

	return nil
}
