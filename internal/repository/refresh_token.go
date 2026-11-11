package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"gorm.io/gorm"
)

var errTokenNotUsed = errors.New("repository: consuming a refresh token the aggregate has not marked used")

type refreshTokenRepository struct {
	db *gorm.DB
}

func (r refreshTokenRepository) Create(ctx context.Context, t *session.RefreshToken) error {
	record := refreshTokenFromDomain(t)

	return translate(r.db.WithContext(ctx).Create(&record).Error, session.ErrTokenNotFound)
}

func (r refreshTokenRepository) FindByHash(
	ctx context.Context, realmID uuid.UUID, hash string,
) (*session.RefreshToken, error) {
	var record refreshTokenRecord

	err := r.db.WithContext(ctx).
		Where("realm_id = ? AND token_hash = ?", realmID.String(), hash).
		Take(&record).Error
	if err != nil {
		return nil, translate(err, session.ErrTokenNotFound)
	}

	return record.toDomain()
}

// The used_at IS NULL predicate is the compare-and-swap: every engine takes
// the row lock and re-reads the committed row before deciding, so a concurrent
// caller that lost the race matches nothing.
func (r refreshTokenRepository) Consume(ctx context.Context, t *session.RefreshToken) (bool, error) {
	usedAt := t.UsedAt()

	// NULL over NULL would count as a row written on Postgres and SQLite and
	// report a token consumed that is still presentable.
	if usedAt == nil {
		return false, errTokenNotUsed
	}

	result := r.db.WithContext(ctx).
		Model(&refreshTokenRecord{}).
		Where(whereRealmAndID, t.RealmID().String(), t.ID().String()).
		Where("used_at IS NULL").
		Update("used_at", Timestamp(*usedAt))
	if result.Error != nil {
		return false, translate(result.Error, session.ErrTokenNotFound)
	}

	return result.RowsAffected > 0, nil
}
