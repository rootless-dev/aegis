package repository

import (
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
)

type refreshTokenRecord struct {
	ID        string `gorm:"column:id;primaryKey"`
	RealmID   string `gorm:"column:realm_id"`
	SessionID string `gorm:"column:session_id"`

	// A pointer: uuid.Nil is the aggregate's "first of its chain", and the
	// foreign key accepts NULL but no row has the nil id.
	ParentID *string `gorm:"column:parent_id"`

	Hash      string     `gorm:"column:token_hash"`
	CreatedAt Timestamp  `gorm:"column:created_at"`
	ExpiresAt Timestamp  `gorm:"column:expires_at"`
	UsedAt    *Timestamp `gorm:"column:used_at"`
}

func (refreshTokenRecord) TableName() string { return "refresh_tokens" }

func refreshTokenFromDomain(t *session.RefreshToken) refreshTokenRecord {
	record := refreshTokenRecord{
		ID:        t.ID().String(),
		RealmID:   t.RealmID().String(),
		SessionID: t.SessionID().String(),
		Hash:      t.Hash(),
		CreatedAt: Timestamp(t.CreatedAt()),
		ExpiresAt: Timestamp(t.ExpiresAt()),
		UsedAt:    nullableTimestamp(t.UsedAt()),
	}

	if t.HasParent() {
		parentID := t.ParentID().String()
		record.ParentID = &parentID
	}

	return record
}

func (rec refreshTokenRecord) toDomain() (*session.RefreshToken, error) {
	id, err := uuid.Parse(rec.ID)
	if err != nil {
		return nil, err
	}

	realmID, err := uuid.Parse(rec.RealmID)
	if err != nil {
		return nil, err
	}

	sessionID, err := uuid.Parse(rec.SessionID)
	if err != nil {
		return nil, err
	}

	parentID := uuid.Nil

	if rec.ParentID != nil {
		if parentID, err = uuid.Parse(*rec.ParentID); err != nil {
			return nil, err
		}
	}

	var usedAt *time.Time

	if rec.UsedAt != nil {
		at := rec.UsedAt.Time()
		usedAt = &at
	}

	return session.RehydrateRefreshToken(
		id,
		realmID,
		sessionID,
		parentID,
		rec.Hash,
		rec.CreatedAt.Time(),
		rec.ExpiresAt.Time(),
		usedAt,
	)
}
