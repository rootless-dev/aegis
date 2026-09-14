package repository

import (
	"context"
	"encoding/base64"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"gorm.io/gorm"
)

type realmKeyRepository struct {
	db *gorm.DB
}

func (r realmKeyRepository) Create(ctx context.Context, k *key.Key) error {
	record, err := keyFromDomain(k)
	if err != nil {
		return err
	}

	return translate(r.db.WithContext(ctx).Create(&record).Error, key.ErrNotFound)
}

func (r realmKeyRepository) FindActive(
	ctx context.Context, realmID uuid.UUID, purpose key.Purpose, algorithm key.Algorithm,
) (*key.Key, error) {
	var record realmKeyRecord

	err := r.db.WithContext(ctx).
		Where(
			"realm_id = ? AND purpose = ? AND algorithm = ? AND status = ?",
			realmID.String(), purpose.String(), algorithm.String(), key.StatusActive.String(),
		).
		Take(&record).Error
	if err != nil {
		return nil, translate(err, key.ErrNoActiveKey)
	}

	return record.toDomain()
}

func (r realmKeyRepository) FindByKID(
	ctx context.Context, realmID uuid.UUID, kid string,
) (*key.Key, error) {
	var record realmKeyRecord

	err := r.db.WithContext(ctx).
		Where("realm_id = ? AND kid = ?", realmID.String(), kid).
		Take(&record).Error
	if err != nil {
		return nil, translate(err, key.ErrNotFound)
	}

	return record.toDomain()
}

func (r realmKeyRepository) ListByRealm(
	ctx context.Context, realmID uuid.UUID, statuses []key.Status,
) ([]*key.Key, error) {
	// Model, because the Where clauses are built before Find binds a destination.
	query := r.db.WithContext(ctx).Model(&realmKeyRecord{}).Where("realm_id = ?", realmID.String())

	if len(statuses) > 0 {
		wanted := make([]string, 0, len(statuses))
		for _, status := range statuses {
			wanted = append(wanted, status.String())
		}

		query = query.Where("status IN ?", wanted)
	}

	var records []realmKeyRecord

	// By id, a UUIDv7, and not by created_at: SQLite sorts that column as RFC 3339
	// text with trailing zeros trimmed, so a whole second sorts after a fraction.
	if err := query.Order("id DESC").Find(&records).Error; err != nil {
		return nil, translate(err, key.ErrNotFound)
	}

	return keysToDomain(records)
}

func (r realmKeyRepository) Update(ctx context.Context, k *key.Key) error {
	// A map and not a struct: only the columns named here are written, whatever
	// fields the record grows later.
	result := r.db.WithContext(ctx).
		Model(&realmKeyRecord{}).
		Where(whereID, k.ID().String()).
		Updates(map[string]any{
			"status":        k.Status().String(),
			"active_marker": markerFor(k.Status()),
			"updated_at":    Timestamp(k.UpdatedAt()),
		})

	return applied(result, key.ErrNotFound)
}

func (r realmKeyRepository) Rewrap(ctx context.Context, k *key.Key) error {
	result := r.db.WithContext(ctx).
		Model(&realmKeyRecord{}).
		Where(whereID, k.ID().String()).
		Updates(map[string]any{
			"private_key": base64.StdEncoding.EncodeToString(k.Sealed()),
			"kek_id":      k.KEKID(),
			"updated_at":  Timestamp(k.UpdatedAt()),
		})

	return applied(result, key.ErrNotFound)
}

func (r realmKeyRepository) ListStaleKEK(
	ctx context.Context, kekID string, limit int,
) ([]*key.Key, error) {
	var records []realmKeyRecord

	err := r.db.WithContext(ctx).
		Where("kek_id <> ?", kekID).
		Order("id").
		Limit(limit).
		Find(&records).Error
	if err != nil {
		return nil, translate(err, key.ErrNotFound)
	}

	return keysToDomain(records)
}

func keysToDomain(records []realmKeyRecord) ([]*key.Key, error) {
	found := make([]*key.Key, 0, len(records))

	for _, record := range records {
		built, err := record.toDomain()
		if err != nil {
			return nil, err
		}

		found = append(found, built)
	}

	return found, nil
}
