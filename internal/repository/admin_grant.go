package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/admin"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

// SELECT EXISTS comes back as a boolean on Postgres and as 0/1 on the other
// three; every driver scans either into a Go bool.
const hasGlobalAdministrator = `
SELECT EXISTS (
    SELECT 1
    FROM admin_grants g
    JOIN identities i ON i.id = g.identity_id AND i.realm_id = g.realm_id
    JOIN credentials c ON c.identity_id = i.id
    WHERE g.realm_id = ?
      AND g.global_marker = ?
      AND i.status = ?
      AND c.type = ?
)`

type adminGrantRepository struct {
	db *gorm.DB
}

func (r adminGrantRepository) Create(ctx context.Context, g *admin.Grant) error {
	record := adminGrantFromDomain(g)

	return translate(r.db.WithContext(ctx).Create(&record).Error, admin.ErrNotFound)
}

func (r adminGrantRepository) ListByIdentity(
	ctx context.Context, realmID, identityID uuid.UUID,
) ([]*admin.Grant, error) {
	var records []adminGrantRecord

	// The id is a UUIDv7, so ascending by it is oldest first.
	err := r.db.WithContext(ctx).
		Where("realm_id = ? AND identity_id = ?", realmID.String(), identityID.String()).
		Order("id ASC").
		Find(&records).Error
	if err != nil {
		return nil, translate(err, admin.ErrNotFound)
	}

	return adminGrantsToDomain(records)
}

func (r adminGrantRepository) ListByRealm(
	ctx context.Context, realmID uuid.UUID, limit int,
) ([]*admin.Grant, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: got %d", service.ErrLimitNotPositive, limit)
	}

	var records []adminGrantRecord

	err := r.db.WithContext(ctx).
		Where("realm_id = ?", realmID.String()).
		Order("id ASC").
		Limit(limit).
		Find(&records).Error
	if err != nil {
		return nil, translate(err, admin.ErrNotFound)
	}

	return adminGrantsToDomain(records)
}

func (r adminGrantRepository) Delete(ctx context.Context, realmID, id uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where(whereRealmAndID, realmID.String(), id.String()).
		Delete(&adminGrantRecord{})

	return applied(result, admin.ErrNotFound)
}

func (r adminGrantRepository) HasGlobalAdministrator(ctx context.Context, realmID uuid.UUID) (bool, error) {
	var found bool

	err := r.db.WithContext(ctx).
		Raw(
			hasGlobalAdministrator,
			realmID.String(), globalMarker, identity.StatusActive.String(), credential.TypePassword.String(),
		).
		Scan(&found).Error
	if err != nil {
		return false, translate(err, admin.ErrNotFound)
	}

	return found, nil
}

func adminGrantsToDomain(records []adminGrantRecord) ([]*admin.Grant, error) {
	grants := make([]*admin.Grant, 0, len(records))

	for _, record := range records {
		found, err := record.toDomain()
		if err != nil {
			return nil, err
		}

		grants = append(grants, found)
	}

	return grants, nil
}
