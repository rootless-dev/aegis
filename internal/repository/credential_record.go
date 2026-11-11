package repository

import (
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
)

type credentialRecord struct {
	ID         string `gorm:"column:id;primaryKey"`
	RealmID    string `gorm:"column:realm_id"`
	IdentityID string `gorm:"column:identity_id"`
	Type       string `gorm:"column:type"`
	Hash       string `gorm:"column:hash"`

	// boolean on Postgres, an integer 0/1 on the other three; every driver
	// converts a Go bool both ways.
	MustChange bool `gorm:"column:must_change"`

	LastUsedAt *Timestamp `gorm:"column:last_used_at"`
	CreatedAt  Timestamp  `gorm:"column:created_at"`
	UpdatedAt  Timestamp  `gorm:"column:updated_at"`
}

func (credentialRecord) TableName() string { return "credentials" }

func credentialFromDomain(c *credential.Credential) credentialRecord {
	return credentialRecord{
		ID:         c.ID().String(),
		RealmID:    c.RealmID().String(),
		IdentityID: c.IdentityID().String(),
		Type:       c.Type().String(),
		Hash:       c.Hash(),
		MustChange: c.MustChange(),
		LastUsedAt: nullableTimestamp(c.LastUsedAt()),
		CreatedAt:  Timestamp(c.CreatedAt()),
		UpdatedAt:  Timestamp(c.UpdatedAt()),
	}
}

func nullableTimestamp(t *time.Time) *Timestamp {
	if t == nil {
		return nil
	}

	stamp := Timestamp(*t)

	return &stamp
}

func (rec credentialRecord) toDomain() (*credential.Credential, error) {
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

	typ, err := credential.ParseType(rec.Type)
	if err != nil {
		return nil, err
	}

	var lastUsedAt *time.Time

	if rec.LastUsedAt != nil {
		at := rec.LastUsedAt.Time()
		lastUsedAt = &at
	}

	return credential.Rehydrate(
		id,
		realmID,
		identityID,
		typ,
		rec.Hash,
		rec.MustChange,
		lastUsedAt,
		rec.CreatedAt.Time(),
		rec.UpdatedAt.Time(),
	)
}
