package repository

import (
	"errors"
	"strings"

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

	switch {
	case strings.Contains(message, "uq_realms_slug"):
		return realm.ErrSlugTaken
	case strings.Contains(message, "uq_realms_issuer"):
		return realm.ErrIssuerTaken
	case strings.Contains(message, "uq_realm_keys_kid"):
		return key.ErrKIDTaken
	case strings.Contains(message, "uq_realm_keys_active"):
		return key.ErrActiveKeyExists
	}

	// SQLite names the columns, not the constraint. Anchored on the prefix and
	// not the column alone: NOT NULL and CHECK violations name it too. Both key
	// indexes lead with realm_id, so the distinctive column tells them apart.
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
		}
	}

	return err
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
