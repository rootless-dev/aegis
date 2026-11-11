package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

// whereRealmAndID is whereID for a tenant-owned row: the realm is part of the
// predicate, so an id from another realm matches nothing.
const whereRealmAndID = "realm_id = ? AND id = ?"

type identityRepository struct {
	db *gorm.DB
}

func (r identityRepository) Create(ctx context.Context, i *identity.Identity) error {
	record := identityFromDomain(i)

	return translate(r.db.WithContext(ctx).Create(&record).Error, identity.ErrNotFound)
}

func (r identityRepository) FindByID(
	ctx context.Context, realmID, id uuid.UUID,
) (*identity.Identity, error) {
	return r.take(ctx, whereRealmAndID, realmID, id.String())
}

func (r identityRepository) FindByEmail(
	ctx context.Context, realmID uuid.UUID, email string,
) (*identity.Identity, error) {
	return r.take(ctx, "realm_id = ? AND email = ?", realmID, email)
}

func (r identityRepository) FindByUsername(
	ctx context.Context, realmID uuid.UUID, username string,
) (*identity.Identity, error) {
	// "" is the aggregate's absent username, never a value to look up.
	if username == "" {
		return nil, identity.ErrNotFound
	}

	return r.take(ctx, "realm_id = ? AND username = ?", realmID, username)
}

func (r identityRepository) take(
	ctx context.Context, where string, realmID uuid.UUID, arg string,
) (*identity.Identity, error) {
	var record identityRecord

	if err := r.db.WithContext(ctx).Where(where, realmID.String(), arg).Take(&record).Error; err != nil {
		return nil, translate(err, identity.ErrNotFound)
	}

	return record.toDomain()
}

func (r identityRepository) List(
	ctx context.Context, realmID uuid.UUID, q service.IdentityQuery,
) ([]*identity.Identity, error) {
	if q.Limit <= 0 {
		return nil, fmt.Errorf("%w: got %d", service.ErrLimitNotPositive, q.Limit)
	}

	query := r.db.WithContext(ctx).
		Model(&identityRecord{}).
		Where("realm_id = ?", realmID.String()).
		Order("id ASC").
		Limit(q.Limit)

	if q.After != uuid.Nil {
		query = query.Where("id > ?", q.After.String())
	}

	if len(q.Status) > 0 {
		wanted := make([]string, 0, len(q.Status))
		for _, status := range q.Status {
			wanted = append(wanted, status.String())
		}

		query = query.Where("status IN ?", wanted)
	}

	var records []identityRecord

	if err := query.Find(&records).Error; err != nil {
		return nil, translate(err, identity.ErrNotFound)
	}

	identities := make([]*identity.Identity, 0, len(records))

	for _, record := range records {
		found, err := record.toDomain()
		if err != nil {
			return nil, err
		}

		identities = append(identities, found)
	}

	return identities, nil
}

func (r identityRepository) Update(ctx context.Context, i *identity.Identity) error {
	result := r.db.WithContext(ctx).
		Model(&identityRecord{}).
		Where(whereRealmAndID, i.RealmID().String(), i.ID().String()).
		Updates(map[string]any{
			"status":     i.Status().String(),
			"updated_at": Timestamp(i.UpdatedAt()),
		})

	return applied(result, identity.ErrNotFound)
}
