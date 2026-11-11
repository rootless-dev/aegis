package repository

import (
	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/identity"
)

type identityRecord struct {
	ID      string `gorm:"column:id;primaryKey"`
	RealmID string `gorm:"column:realm_id"`
	Email   string `gorm:"column:email"`

	// A pointer: the aggregate's "no username" is the empty string, and two of
	// those would collide in uq_identities_username where two NULLs do not.
	Username *string `gorm:"column:username"`

	Status    string    `gorm:"column:status"`
	CreatedAt Timestamp `gorm:"column:created_at"`
	UpdatedAt Timestamp `gorm:"column:updated_at"`
}

func (identityRecord) TableName() string { return "identities" }

func identityFromDomain(i *identity.Identity) identityRecord {
	record := identityRecord{
		ID:        i.ID().String(),
		RealmID:   i.RealmID().String(),
		Email:     i.Email(),
		Status:    i.Status().String(),
		CreatedAt: Timestamp(i.CreatedAt()),
		UpdatedAt: Timestamp(i.UpdatedAt()),
	}

	if i.HasUsername() {
		username := i.Username()
		record.Username = &username
	}

	return record
}

func (rec identityRecord) toDomain() (*identity.Identity, error) {
	id, err := uuid.Parse(rec.ID)
	if err != nil {
		return nil, err
	}

	realmID, err := uuid.Parse(rec.RealmID)
	if err != nil {
		return nil, err
	}

	status, err := identity.ParseStatus(rec.Status)
	if err != nil {
		return nil, err
	}

	username := ""
	if rec.Username != nil {
		username = *rec.Username
	}

	return identity.Rehydrate(
		id,
		realmID,
		rec.Email,
		username,
		status,
		rec.CreatedAt.Time(),
		rec.UpdatedAt.Time(),
	)
}
