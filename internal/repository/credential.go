package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"gorm.io/gorm"
)

type credentialRepository struct {
	db *gorm.DB
}

func (r credentialRepository) Create(ctx context.Context, c *credential.Credential) error {
	record := credentialFromDomain(c)

	return translate(r.db.WithContext(ctx).Create(&record).Error, credential.ErrNotFound)
}

func (r credentialRepository) Find(
	ctx context.Context, realmID, identityID uuid.UUID, typ credential.Type,
) (*credential.Credential, error) {
	var record credentialRecord

	err := r.db.WithContext(ctx).
		Where(
			"realm_id = ? AND identity_id = ? AND type = ?",
			realmID.String(), identityID.String(), typ.String(),
		).
		Take(&record).Error
	if err != nil {
		return nil, translate(err, credential.ErrNotFound)
	}

	return record.toDomain()
}

func (r credentialRepository) Update(ctx context.Context, c *credential.Credential) error {
	result := r.db.WithContext(ctx).
		Model(&credentialRecord{}).
		Where(whereRealmAndID, c.RealmID().String(), c.ID().String()).
		Updates(credentialColumns(c))

	return applied(result, credential.ErrNotFound)
}

func (r credentialRepository) UpdateIfHash(
	ctx context.Context, c *credential.Credential, previousHash string,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&credentialRecord{}).
		Where(whereRealmAndID, c.RealmID().String(), c.ID().String()).
		Where("hash = ?", previousHash).
		Updates(credentialColumns(c))
	if result.Error != nil {
		return false, translate(result.Error, credential.ErrNotFound)
	}

	return result.RowsAffected > 0, nil
}

// A map because struct Updates skips zero values: must_change=false and a NULL
// last_used_at would never be written.
func credentialColumns(c *credential.Credential) map[string]any {
	// An untyped nil for NULL: a nil *Timestamp in the map would reach the
	// driver as a non-nil Valuer.
	var lastUsedAt any
	if at := c.LastUsedAt(); at != nil {
		lastUsedAt = Timestamp(*at)
	}

	return map[string]any{
		"hash":         c.Hash(),
		"must_change":  c.MustChange(),
		"last_used_at": lastUsedAt,
		"updated_at":   Timestamp(c.UpdatedAt()),
	}
}
