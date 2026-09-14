package key

import (
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Private fields with accessors: a key's identity is immutable by compiler,
// not by convention.
type Key struct {
	id        uuid.UUID
	realmID   uuid.UUID
	kid       string
	purpose   Purpose
	algorithm Algorithm
	status    Status
	publicKey crypto.PublicKey
	sealed    []byte
	kekID     string
	createdAt time.Time
	updatedAt time.Time
}

// New generates the identifier, derives the kid from the public key, and
// stamps both timestamps to the same UTC instant.
//
// A key is born active and there is no Activate, which leaves an insert as the
// single writer of active_marker.
func New(
	realmID uuid.UUID,
	purpose Purpose,
	algorithm Algorithm,
	publicKey crypto.PublicKey,
	sealed []byte,
	kekID string,
) (*Key, error) {
	if realmID == uuid.Nil {
		return nil, errors.New("key: a key needs a realm")
	}

	if _, err := ParsePurpose(purpose.String()); err != nil {
		return nil, err
	}

	if _, err := ParseAlgorithm(algorithm.String()); err != nil {
		return nil, err
	}

	if !algorithm.Matches(publicKey) {
		return nil, fmt.Errorf("key: %T is not a %s key", publicKey, algorithm)
	}

	if len(sealed) == 0 {
		return nil, errors.New("key: the sealed private material is empty")
	}

	if kekID == "" {
		return nil, errors.New("key: the sealed material has no kek id, so nothing could open it")
	}

	kid, err := Thumbprint(publicKey)
	if err != nil {
		return nil, err
	}

	// NewV7 reads the clock and the entropy source; uuid.Must would turn a
	// transient failure into a panic inside a request.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("key: generating an identifier: %w", err)
	}

	now := nowUTC()

	return &Key{
		id:        id,
		realmID:   realmID,
		kid:       kid,
		purpose:   purpose,
		algorithm: algorithm,
		status:    StatusActive,
		publicKey: publicKey,
		sealed:    sealed,
		kekID:     kekID,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// Rehydrate is the repository's door back in. It takes the stored kid as
// given: recomputing an identity on every read is how a stored value and a
// derived value drift apart with no symptom.
// Stored is one row as the repository read it, named so the eleven values a
// key is made of arrive as a record rather than as an argument list nobody can
// call without counting.
type Stored struct {
	ID      uuid.UUID
	RealmID uuid.UUID
	KID     string

	Purpose   Purpose
	Algorithm Algorithm
	Status    Status

	PublicKeyDER []byte
	Sealed       []byte
	KEKID        string

	CreatedAt time.Time
	UpdatedAt time.Time
}

func Rehydrate(row Stored) (*Key, error) {
	id, realmID, kid := row.ID, row.RealmID, row.KID
	purpose, algorithm, status := row.Purpose, row.Algorithm, row.Status
	publicKeyDER, sealed, kekID := row.PublicKeyDER, row.Sealed, row.KEKID
	createdAt, updatedAt := row.CreatedAt, row.UpdatedAt

	if id == uuid.Nil || realmID == uuid.Nil {
		return nil, errors.New("key: a stored key needs an id and a realm")
	}

	if kid == "" {
		return nil, errors.New("key: a stored key needs a kid")
	}

	if _, err := ParsePurpose(purpose.String()); err != nil {
		return nil, err
	}

	if _, err := ParseAlgorithm(algorithm.String()); err != nil {
		return nil, err
	}

	if _, err := ParseStatus(status.String()); err != nil {
		return nil, err
	}

	publicKey, err := x509.ParsePKIXPublicKey(publicKeyDER)
	if err != nil {
		return nil, fmt.Errorf("key: reading the stored public key: %w", err)
	}

	if !algorithm.Matches(publicKey) {
		return nil, fmt.Errorf("key: the stored public key is not a %s key", algorithm)
	}

	if len(sealed) == 0 || kekID == "" {
		return nil, errors.New("key: the stored private material is incomplete")
	}

	return &Key{
		id:        id,
		realmID:   realmID,
		kid:       kid,
		purpose:   purpose,
		algorithm: algorithm,
		status:    status,
		publicKey: publicKey,
		sealed:    sealed,
		kekID:     kekID,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

func (k *Key) ID() uuid.UUID               { return k.id }
func (k *Key) RealmID() uuid.UUID          { return k.realmID }
func (k *Key) KID() string                 { return k.kid }
func (k *Key) Purpose() Purpose            { return k.purpose }
func (k *Key) Algorithm() Algorithm        { return k.algorithm }
func (k *Key) Status() Status              { return k.status }
func (k *Key) PublicKey() crypto.PublicKey { return k.publicKey }
func (k *Key) KEKID() string               { return k.kekID }
func (k *Key) CreatedAt() time.Time        { return k.createdAt }
func (k *Key) UpdatedAt() time.Time        { return k.updatedAt }

func (k *Key) Ref() Ref { return Ref{KID: k.kid, Algorithm: k.algorithm} }

// Sealed returns a copy: a caller holding the backing array could change the
// ciphertext the aggregate believes it has.
func (k *Key) Sealed() []byte {
	out := make([]byte, len(k.sealed))
	copy(out, k.sealed)

	return out
}

// Marshals on demand rather than storing a second copy: PKIX DER is
// deterministic, so there is no pair that can drift.
func (k *Key) PublicKeyDER() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(k.publicKey)
	if err != nil {
		return nil, fmt.Errorf("key: marshalling the public key: %w", err)
	}

	return der, nil
}

// Rewrapped records a resealing the caller already did; no plaintext enters
// this package.
func (k *Key) Rewrapped(sealed []byte, kekID string) error {
	if len(sealed) == 0 || kekID == "" {
		return errors.New("key: a rewrap needs sealed material and a kek id")
	}

	k.sealed = sealed
	k.kekID = kekID
	k.touch()

	return nil
}

func (k *Key) Deactivate() error {
	if k.status != StatusActive {
		return fmt.Errorf("%w: %s cannot be deactivated", ErrInvalidTransition, k.status)
	}

	k.status = StatusPassive
	k.touch()

	return nil
}

func (k *Key) Disable() error {
	// Refused on purpose: disabling the active key would leave the realm with
	// nothing able to sign.
	if k.status != StatusPassive {
		return fmt.Errorf("%w: %s cannot be disabled; rotate first", ErrInvalidTransition, k.status)
	}

	k.status = StatusDisabled
	k.touch()

	return nil
}

func (k *Key) Enable() error {
	if k.status != StatusDisabled {
		return fmt.Errorf("%w: %s cannot be enabled", ErrInvalidTransition, k.status)
	}

	// Passive and not active: putting a key back into service is a rotation.
	k.status = StatusPassive
	k.touch()

	return nil
}

func (k *Key) touch() { k.updatedAt = nowUTC() }

// Microsecond precision: a value the database cannot hold would read back
// different from what was written.
func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
