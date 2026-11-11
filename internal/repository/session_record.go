package repository

import (
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
)

type sessionRecord struct {
	ID         string `gorm:"column:id;primaryKey"`
	RealmID    string `gorm:"column:realm_id"`
	IdentityID string `gorm:"column:identity_id"`

	// Pointers: the aggregate's "" is NULL in the column.
	IP        *string `gorm:"column:ip"`
	UserAgent *string `gorm:"column:user_agent"`

	CreatedAt  Timestamp  `gorm:"column:created_at"`
	LastSeenAt Timestamp  `gorm:"column:last_seen_at"`
	ExpiresAt  Timestamp  `gorm:"column:expires_at"`
	EndedAt    *Timestamp `gorm:"column:ended_at"`
	EndReason  *string    `gorm:"column:end_reason"`
}

func (sessionRecord) TableName() string { return "sessions" }

func sessionFromDomain(s *session.Session) sessionRecord {
	return sessionRecord{
		ID:         s.ID().String(),
		RealmID:    s.RealmID().String(),
		IdentityID: s.IdentityID().String(),
		IP:         nullableString(s.IP()),
		UserAgent:  nullableString(s.UserAgent()),
		CreatedAt:  Timestamp(s.CreatedAt()),
		LastSeenAt: Timestamp(s.LastSeenAt()),
		ExpiresAt:  Timestamp(s.ExpiresAt()),
		EndedAt:    nullableTimestamp(s.EndedAt()),
		EndReason:  nullableString(s.EndReason().String()),
	}
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

func (rec sessionRecord) toDomain() (*session.Session, error) {
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

	var endedAt *time.Time

	if rec.EndedAt != nil {
		at := rec.EndedAt.Time()
		endedAt = &at
	}

	return session.Rehydrate(
		id,
		realmID,
		identityID,
		stringOrEmpty(rec.IP),
		stringOrEmpty(rec.UserAgent),
		rec.CreatedAt.Time(),
		rec.LastSeenAt.Time(),
		rec.ExpiresAt.Time(),
		endedAt,
		session.EndReason(stringOrEmpty(rec.EndReason)),
	)
}
