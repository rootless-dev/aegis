package repository

import (
	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/admin"
)

// Set only on a global grant, so that a plain unique index on the identity and
// the marker carries the one-global-grant rule: NULLs do not collide.
const globalMarker = "y"

type adminGrantRecord struct {
	ID         string `gorm:"column:id;primaryKey"`
	RealmID    string `gorm:"column:realm_id"`
	IdentityID string `gorm:"column:identity_id"`

	// Exactly one of the two is set; ck_admin_grants_scope refuses anything else.
	TargetRealmID *string `gorm:"column:target_realm_id"`
	GlobalMarker  *string `gorm:"column:global_marker"`

	CreatedAt Timestamp `gorm:"column:created_at"`
}

func (adminGrantRecord) TableName() string { return "admin_grants" }

func adminGrantFromDomain(g *admin.Grant) adminGrantRecord {
	record := adminGrantRecord{
		ID:         g.ID().String(),
		RealmID:    g.RealmID().String(),
		IdentityID: g.IdentityID().String(),
		CreatedAt:  Timestamp(g.CreatedAt()),
	}

	if g.Global() {
		marker := globalMarker
		record.GlobalMarker = &marker
	} else {
		target := g.TargetRealmID().String()
		record.TargetRealmID = &target
	}

	return record
}

func (rec adminGrantRecord) toDomain() (*admin.Grant, error) {
	id, err := uuid.Parse(rec.ID)
	if err != nil {
		return nil, err
	}

	realmID, err := uuid.Parse(rec.RealmID)
	if err != nil {
		return nil, err
	}

	identityID, err := uuid.Parse(rec.IdentityID)
	if err != nil {
		return nil, err
	}

	targetRealmID := uuid.Nil

	if rec.TargetRealmID != nil {
		targetRealmID, err = uuid.Parse(*rec.TargetRealmID)
		if err != nil {
			return nil, err
		}
	}

	return admin.Rehydrate(id, realmID, identityID, targetRealmID, rec.CreatedAt.Time())
}
