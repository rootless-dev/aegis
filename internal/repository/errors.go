package repository

import (
	"errors"
	"strings"

	"github.com/rootless-dev/aegis/internal/domain/realm"
	"gorm.io/gorm"
)

// translate keeps gorm.ErrRecordNotFound and every driver's unique-violation
// text inside this package.
func translate(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return realm.ErrNotFound
	}

	// Keyed off the constraint name: no engine exposes the violation as a typed
	// value the four share.
	message := strings.ToLower(err.Error())

	switch {
	case strings.Contains(message, "uq_realms_slug"):
		return realm.ErrSlugTaken
	case strings.Contains(message, "uq_realms_issuer"):
		return realm.ErrIssuerTaken
	}

	// SQLite names the column, not the constraint. Anchored on the prefix and
	// not the column alone: NOT NULL and CHECK violations name it too.
	if strings.Contains(message, "unique constraint failed: ") {
		switch {
		case strings.Contains(message, "realms.slug"):
			return realm.ErrSlugTaken
		case strings.Contains(message, "realms.issuer"):
			return realm.ErrIssuerTaken
		}
	}

	return err
}
