package repository

import (
	"encoding/base64"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
)

// Set only on an active row, so that NULLs, which do not collide in a unique
// index on any of the four engines, carry the one-active-key rule.
const activeMarker = "y"

type realmKeyRecord struct {
	ID        string `gorm:"column:id;primaryKey"`
	RealmID   string `gorm:"column:realm_id"`
	KID       string `gorm:"column:kid"`
	Purpose   string `gorm:"column:purpose"`
	Algorithm string `gorm:"column:algorithm"`
	Status    string `gorm:"column:status"`

	// A pointer: an empty string would collide in the unique index, a NULL does not.
	ActiveMarker *string `gorm:"column:active_marker"`

	PublicKey  string    `gorm:"column:public_key"`
	PrivateKey string    `gorm:"column:private_key"`
	KEKID      string    `gorm:"column:kek_id"`
	CreatedAt  Timestamp `gorm:"column:created_at"`
	UpdatedAt  Timestamp `gorm:"column:updated_at"`
}

func (realmKeyRecord) TableName() string { return "realm_keys" }

func keyFromDomain(k *key.Key) (realmKeyRecord, error) {
	publicKey, err := k.PublicKeyDER()
	if err != nil {
		return realmKeyRecord{}, err
	}

	return realmKeyRecord{
		ID:        k.ID().String(),
		RealmID:   k.RealmID().String(),
		KID:       k.KID(),
		Purpose:   k.Purpose().String(),
		Algorithm: k.Algorithm().String(),
		Status:    k.Status().String(),

		ActiveMarker: markerFor(k.Status()),

		PublicKey:  base64.StdEncoding.EncodeToString(publicKey),
		PrivateKey: base64.StdEncoding.EncodeToString(k.Sealed()),
		KEKID:      k.KEKID(),
		CreatedAt:  Timestamp(k.CreatedAt()),
		UpdatedAt:  Timestamp(k.UpdatedAt()),
	}, nil
}

func markerFor(status key.Status) *string {
	if status != key.StatusActive {
		return nil
	}

	marker := activeMarker

	return &marker
}

func (rec realmKeyRecord) toDomain() (*key.Key, error) {
	id, err := uuid.Parse(rec.ID)
	if err != nil {
		return nil, err
	}

	realmID, err := uuid.Parse(rec.RealmID)
	if err != nil {
		return nil, err
	}

	purpose, err := key.ParsePurpose(rec.Purpose)
	if err != nil {
		return nil, err
	}

	algorithm, err := key.ParseAlgorithm(rec.Algorithm)
	if err != nil {
		return nil, err
	}

	status, err := key.ParseStatus(rec.Status)
	if err != nil {
		return nil, err
	}

	publicKey, err := base64.StdEncoding.DecodeString(rec.PublicKey)
	if err != nil {
		return nil, err
	}

	sealed, err := base64.StdEncoding.DecodeString(rec.PrivateKey)
	if err != nil {
		return nil, err
	}

	return key.Rehydrate(key.Stored{
		ID:      id,
		RealmID: realmID,
		KID:     rec.KID,

		Purpose:   purpose,
		Algorithm: algorithm,
		Status:    status,

		PublicKeyDER: publicKey,
		Sealed:       sealed,
		KEKID:        rec.KEKID,

		CreatedAt: rec.CreatedAt.Time(),
		UpdatedAt: rec.UpdatedAt.Time(),
	})
}
