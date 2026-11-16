// Package admin holds who may administer what. It imports nothing beyond the
// standard library and uuid.
package admin

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// realmID is the realm the identity lives in; targetRealmID is the realm the
// grant applies to, uuid.Nil when it applies to all of them.
type Grant struct {
	id            uuid.UUID
	realmID       uuid.UUID
	identityID    uuid.UUID
	targetRealmID uuid.UUID
	createdAt     time.Time
}

func NewGlobal(realmID, identityID uuid.UUID) (*Grant, error) {
	return newGrant(realmID, identityID, uuid.Nil)
}

func NewForRealm(realmID, identityID, targetRealmID uuid.UUID) (*Grant, error) {
	if targetRealmID == uuid.Nil {
		return nil, ErrTargetInvalid
	}

	return newGrant(realmID, identityID, targetRealmID)
}

func newGrant(realmID, identityID, targetRealmID uuid.UUID) (*Grant, error) {
	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if identityID == uuid.Nil {
		return nil, ErrIdentityInvalid
	}

	// NewV7 reads the clock and the entropy source; uuid.Must would turn a
	// transient failure into a panic inside a request.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("admin: generating an identifier: %w", err)
	}

	return &Grant{
		id:            id,
		realmID:       realmID,
		identityID:    identityID,
		targetRealmID: targetRealmID,
		// Microsecond precision: a value the database cannot hold would read
		// back different from what was written.
		createdAt: time.Now().UTC().Truncate(time.Microsecond),
	}, nil
}

// Rehydrate is the repository's door back in. A nil target is a global grant.
func Rehydrate(id, realmID, identityID, targetRealmID uuid.UUID, createdAt time.Time) (*Grant, error) {
	if id == uuid.Nil {
		return nil, ErrIDInvalid
	}

	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if identityID == uuid.Nil {
		return nil, ErrIdentityInvalid
	}

	return &Grant{
		id:            id,
		realmID:       realmID,
		identityID:    identityID,
		targetRealmID: targetRealmID,
		createdAt:     createdAt.UTC(),
	}, nil
}

func (g *Grant) ID() uuid.UUID            { return g.id }
func (g *Grant) RealmID() uuid.UUID       { return g.realmID }
func (g *Grant) IdentityID() uuid.UUID    { return g.identityID }
func (g *Grant) TargetRealmID() uuid.UUID { return g.targetRealmID }
func (g *Grant) Global() bool             { return g.targetRealmID == uuid.Nil }
func (g *Grant) CreatedAt() time.Time     { return g.createdAt }

// Covers reports whether this grant allows administering the realm. The nil
// realm is refused first: a global grant would otherwise cover it.
func (g *Grant) Covers(realmID uuid.UUID) bool {
	if realmID == uuid.Nil {
		return false
	}

	return g.Global() || g.targetRealmID == realmID
}
