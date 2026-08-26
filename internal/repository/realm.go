package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

// whereID is the predicate every single-row operation in this file shares.
const whereID = "id = ?"

type realmRepository struct {
	db *gorm.DB
}

func (r realmRepository) Create(ctx context.Context, aggregate *realm.Realm) error {
	record := fromDomain(aggregate)

	return translate(r.db.WithContext(ctx).Create(&record).Error)
}

func (r realmRepository) FindByID(ctx context.Context, id uuid.UUID) (*realm.Realm, error) {
	if id == uuid.Nil {
		return nil, realm.ErrIDInvalid
	}

	return r.first(ctx, whereID, id.String())
}

func (r realmRepository) FindBySlug(ctx context.Context, slug string) (*realm.Realm, error) {
	return r.first(ctx, "slug = ?", slug)
}

func (r realmRepository) first(ctx context.Context, where string, arg any) (*realm.Realm, error) {
	var record realmRecord

	if err := r.db.WithContext(ctx).Where(where, arg).First(&record).Error; err != nil {
		return nil, translate(err)
	}

	return record.toDomain()
}

// List pages by keyset. `id > ?` orders the same way on all four engines only
// because the schema forces utf8mb4_bin on MySQL and MariaDB — a
// case-insensitive collation would make this skip rows on one engine alone.
func (r realmRepository) List(ctx context.Context, q service.RealmQuery) ([]*realm.Realm, error) {
	// Refused rather than given a meaning here. The service decides what an
	// unset limit becomes, and a second default at this boundary would be a
	// second answer to the same question — the one a caller reaching past the
	// service would silently get instead.
	if q.Limit <= 0 {
		return nil, fmt.Errorf("%w: got %d", service.ErrLimitNotPositive, q.Limit)
	}

	query := r.db.WithContext(ctx).Model(&realmRecord{}).Order("id ASC").Limit(q.Limit)

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

	var records []realmRecord

	if err := query.Find(&records).Error; err != nil {
		return nil, translate(err)
	}

	aggregates := make([]*realm.Realm, 0, len(records))

	for _, record := range records {
		aggregate, err := record.toDomain()
		if err != nil {
			return nil, err
		}

		aggregates = append(aggregates, aggregate)
	}

	return aggregates, nil
}

// Names its columns rather than saving the record, so a setter added to the
// aggregate later cannot reach slug or issuer through here.
func (r realmRepository) Update(ctx context.Context, aggregate *realm.Realm) error {
	result := r.db.WithContext(ctx).
		Model(&realmRecord{}).
		Where(whereID, aggregate.ID().String()).
		Updates(map[string]any{
			"display_name": aggregate.DisplayName(),
			"status":       aggregate.Status().String(),
			"updated_at":   Timestamp(aggregate.UpdatedAt()),
		})

	if result.Error != nil {
		return translate(result.Error)
	}

	if result.RowsAffected == 0 {
		return realm.ErrNotFound
	}

	return nil
}

// Separate from Update because the issuer is immutable by design. Two callers:
// the development seed, and the domain-migration command that does not exist
// yet.
func (r realmRepository) Reissue(ctx context.Context, id uuid.UUID, issuer string) error {
	result := r.db.WithContext(ctx).
		Model(&realmRecord{}).
		Where(whereID, id.String()).
		Updates(map[string]any{
			"issuer":     issuer,
			"updated_at": Timestamp(nowUTC()),
		})

	if result.Error != nil {
		return translate(result.Error)
	}

	if result.RowsAffected == 0 {
		return realm.ErrNotFound
	}

	return nil
}
