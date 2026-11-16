package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

const whereLive = "ended_at IS NULL"

type sessionRepository struct {
	db *gorm.DB
}

func (r sessionRepository) Create(ctx context.Context, s *session.Session) error {
	record := sessionFromDomain(s)

	return translate(r.db.WithContext(ctx).Create(&record).Error, session.ErrNotFound)
}

func (r sessionRepository) Find(ctx context.Context, realmID, id uuid.UUID) (*session.Session, error) {
	var record sessionRecord

	err := r.db.WithContext(ctx).
		Where(whereRealmAndID, realmID.String(), id.String()).
		Take(&record).Error
	if err != nil {
		return nil, translate(err, session.ErrNotFound)
	}

	return record.toDomain()
}

func (r sessionRepository) ListByIdentity(
	ctx context.Context, realmID, identityID uuid.UUID, limit int,
) ([]*session.Session, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: got %d", service.ErrLimitNotPositive, limit)
	}

	var records []sessionRecord

	// The id is a UUIDv7, so descending by it is newest first.
	err := r.db.WithContext(ctx).
		Where("realm_id = ? AND identity_id = ?", realmID.String(), identityID.String()).
		Order("id DESC").
		Limit(limit).
		Find(&records).Error
	if err != nil {
		return nil, translate(err, session.ErrNotFound)
	}

	sessions := make([]*session.Session, 0, len(records))

	for _, record := range records {
		found, err := record.toDomain()
		if err != nil {
			return nil, err
		}

		sessions = append(sessions, found)
	}

	return sessions, nil
}

func (r sessionRepository) Touch(ctx context.Context, s *session.Session) error {
	err := r.db.WithContext(ctx).
		Model(&sessionRecord{}).
		Where(whereRealmAndID, s.RealmID().String(), s.ID().String()).
		Where(whereLive).
		Update("last_seen_at", Timestamp(s.LastSeenAt())).Error

	return translate(err, session.ErrNotFound)
}

func (r sessionRepository) End(ctx context.Context, s *session.Session) (bool, error) {
	endedAt := s.EndedAt()

	// A live aggregate would write NULL over NULL, which Postgres and SQLite
	// count as a row written.
	if endedAt == nil {
		return false, session.ErrEndInvalid
	}

	ended, err := r.end(
		r.db.WithContext(ctx).Where(whereRealmAndID, s.RealmID().String(), s.ID().String()),
		*endedAt, s.EndReason(),
	)

	return ended > 0, err
}

func (r sessionRepository) EndAllByIdentity(
	ctx context.Context, realmID, identityID uuid.UUID, at time.Time, reason session.EndReason,
) (int64, error) {
	return r.end(
		r.db.WithContext(ctx).Where("realm_id = ? AND identity_id = ?", realmID.String(), identityID.String()),
		at, reason,
	)
}

func (r sessionRepository) EndAllByRealm(
	ctx context.Context, realmID uuid.UUID, at time.Time, reason session.EndReason,
) (int64, error) {
	return r.end(r.db.WithContext(ctx).Where("realm_id = ?", realmID.String()), at, reason)
}

func (r sessionRepository) end(scope *gorm.DB, at time.Time, reason session.EndReason) (int64, error) {
	result := scope.
		Model(&sessionRecord{}).
		Where(whereLive).
		Updates(map[string]any{
			"ended_at":   Timestamp(at),
			"end_reason": reason.String(),
		})
	if result.Error != nil {
		return 0, translate(result.Error, session.ErrNotFound)
	}

	return result.RowsAffected, nil
}
