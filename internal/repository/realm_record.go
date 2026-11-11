package repository

import (
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

// No gorm type tag on any field: GORM never generates DDL here, so a tag would
// be a second description of a column the migrations already define.
type realmRecord struct {
	ID          string `gorm:"column:id;primaryKey"`
	Slug        string `gorm:"column:slug"`
	DisplayName string `gorm:"column:display_name"`
	Issuer      string `gorm:"column:issuer"`
	Status      string `gorm:"column:status"`

	// Whole seconds.
	AccessTokenTTL int64 `gorm:"column:access_token_ttl"`
	SessionIdleTTL int64 `gorm:"column:session_idle_ttl"`
	SessionMaxTTL  int64 `gorm:"column:session_max_ttl"`

	// boolean on Postgres, an integer 0/1 on the other three.
	PasswordGrant bool `gorm:"column:password_grant"`

	CreatedAt Timestamp `gorm:"column:created_at"`
	UpdatedAt Timestamp `gorm:"column:updated_at"`
}

func (realmRecord) TableName() string { return "realms" }

func fromDomain(r *realm.Realm) realmRecord {
	settings := r.Settings()

	return realmRecord{
		// Canonical 36-character form on every dialect, Postgres included, where
		// the driver casts it to uuid.
		ID:             r.ID().String(),
		Slug:           r.Slug(),
		DisplayName:    r.DisplayName(),
		Issuer:         r.Issuer(),
		Status:         r.Status().String(),
		AccessTokenTTL: int64(settings.AccessTokenTTL / time.Second),
		SessionIdleTTL: int64(settings.SessionIdleTTL / time.Second),
		SessionMaxTTL:  int64(settings.SessionMaxTTL / time.Second),
		PasswordGrant:  settings.PasswordGrant,
		CreatedAt:      Timestamp(r.CreatedAt()),
		UpdatedAt:      Timestamp(r.UpdatedAt()),
	}
}

// Saturates instead of multiplying blindly: a stored value large enough to
// overflow would wrap into a duration that could pass validation.
func secondsToDuration(seconds int64) time.Duration {
	if seconds > int64(math.MaxInt64/time.Second) {
		return math.MaxInt64
	}

	return time.Duration(seconds) * time.Second
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
		realm.Settings{
			AccessTokenTTL: secondsToDuration(rec.AccessTokenTTL),
			SessionIdleTTL: secondsToDuration(rec.SessionIdleTTL),
			SessionMaxTTL:  secondsToDuration(rec.SessionMaxTTL),
			PasswordGrant:  rec.PasswordGrant,
		},
		rec.CreatedAt.Time(),
		rec.UpdatedAt.Time(),
	)
}
