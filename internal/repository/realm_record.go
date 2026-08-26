package repository

import (
	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

// No gorm type tag on any field: GORM never generates DDL here, so a tag would
// be a second description of a column the migrations already define.
type realmRecord struct {
	ID          string    `gorm:"column:id;primaryKey"`
	Slug        string    `gorm:"column:slug"`
	DisplayName string    `gorm:"column:display_name"`
	Issuer      string    `gorm:"column:issuer"`
	Status      string    `gorm:"column:status"`
	CreatedAt   Timestamp `gorm:"column:created_at"`
	UpdatedAt   Timestamp `gorm:"column:updated_at"`
}

func (realmRecord) TableName() string { return "realms" }

func fromDomain(r *realm.Realm) realmRecord {
	return realmRecord{
		// Canonical 36-character form on every dialect, Postgres included, where
		// the driver casts it to uuid.
		ID:          r.ID().String(),
		Slug:        r.Slug(),
		DisplayName: r.DisplayName(),
		Issuer:      r.Issuer(),
		Status:      r.Status().String(),
		CreatedAt:   Timestamp(r.CreatedAt()),
		UpdatedAt:   Timestamp(r.UpdatedAt()),
	}
}

// Through Rehydrate, which validates: a customer's DBA writes here directly.
func (rec realmRecord) toDomain() (*realm.Realm, error) {
	id, err := uuid.Parse(rec.ID)
	if err != nil {
		return nil, err
	}

	status, err := realm.ParseStatus(rec.Status)
	if err != nil {
		return nil, err
	}

	return realm.Rehydrate(
		id,
		rec.Slug,
		rec.DisplayName,
		rec.Issuer,
		status,
		rec.CreatedAt.Time(),
		rec.UpdatedAt.Time(),
	)
}
