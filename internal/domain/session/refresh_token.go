package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	secretBytes = 32
	hashLength  = sha256.Size * 2
)

// Only the hash of the secret is kept, so a leaked table yields nothing that
// can be presented. A token is only as valid as its session.
type RefreshToken struct {
	id        uuid.UUID
	realmID   uuid.UUID
	sessionID uuid.UUID
	parentID  uuid.UUID
	hash      string
	createdAt time.Time
	expiresAt time.Time
	usedAt    *time.Time
}

// GenerateSecret returns a new opaque refresh token and the hash to store.
func GenerateSecret() (secret, hash string, err error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("session: generating a refresh token: %w", err)
	}

	secret = base64.RawURLEncoding.EncodeToString(raw)

	return secret, HashSecret(secret), nil
}

// HashSecret is plain SHA-256 on purpose: the secret is 256 random bits, so
// there is no guessable input for a slow hash to protect.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))

	return hex.EncodeToString(sum[:])
}

// NewRefreshToken takes uuid.Nil as parentID for the first token of a chain.
func NewRefreshToken(
	realmID, sessionID, parentID uuid.UUID,
	hash string,
	now, expiresAt time.Time,
) (*RefreshToken, error) {
	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if sessionID == uuid.Nil {
		return nil, ErrSessionInvalid
	}

	if err := validateHash(hash); err != nil {
		return nil, err
	}

	createdAt, expiresAt := microUTC(now), microUTC(expiresAt)

	if !expiresAt.After(createdAt) {
		return nil, ErrTTLInvalid
	}

	// NewV7 reads the clock and the entropy source; uuid.Must would turn a
	// transient failure into a panic inside a request.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("session: generating an identifier: %w", err)
	}

	return &RefreshToken{
		id:        id,
		realmID:   realmID,
		sessionID: sessionID,
		parentID:  parentID,
		hash:      hash,
		createdAt: createdAt,
		expiresAt: expiresAt,
	}, nil
}

// RehydrateRefreshToken is the repository's door back in.
func RehydrateRefreshToken(
	id, realmID, sessionID, parentID uuid.UUID,
	hash string,
	createdAt, expiresAt time.Time,
	usedAt *time.Time,
) (*RefreshToken, error) {
	if id == uuid.Nil {
		return nil, ErrIDInvalid
	}

	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if sessionID == uuid.Nil {
		return nil, ErrSessionInvalid
	}

	if err := validateHash(hash); err != nil {
		return nil, err
	}

	if !expiresAt.After(createdAt) {
		return nil, ErrTTLInvalid
	}

	return &RefreshToken{
		id:        id,
		realmID:   realmID,
		sessionID: sessionID,
		parentID:  parentID,
		hash:      hash,
		createdAt: microUTC(createdAt),
		expiresAt: microUTC(expiresAt),
		usedAt:    microUTCCopy(usedAt),
	}, nil
}

func (t *RefreshToken) ID() uuid.UUID        { return t.id }
func (t *RefreshToken) RealmID() uuid.UUID   { return t.realmID }
func (t *RefreshToken) SessionID() uuid.UUID { return t.sessionID }
func (t *RefreshToken) ParentID() uuid.UUID  { return t.parentID }
func (t *RefreshToken) HasParent() bool      { return t.parentID != uuid.Nil }
func (t *RefreshToken) Hash() string         { return t.hash }
func (t *RefreshToken) CreatedAt() time.Time { return t.createdAt }
func (t *RefreshToken) ExpiresAt() time.Time { return t.expiresAt }
func (t *RefreshToken) Used() bool           { return t.usedAt != nil }

// UsedAt returns a copy: a caller holding the pointer could rewrite the
// aggregate's history.
func (t *RefreshToken) UsedAt() *time.Time { return microUTCCopy(t.usedAt) }

// Expired treats the deadline as exclusive, like the session's.
func (t *RefreshToken) Expired(now time.Time) bool { return !now.Before(t.expiresAt) }

func (t *RefreshToken) Use(now time.Time) error {
	if t.Used() {
		return ErrTokenUsed
	}

	at := microUTC(now)
	t.usedAt = &at

	return nil
}

// The messages never quote the value: a hash must not reach a log.
func validateHash(hash string) error {
	if len(hash) != hashLength {
		return fmt.Errorf("%w: %d characters, want %d", ErrHashInvalid, len(hash), hashLength)
	}

	for i := range len(hash) {
		if c := hash[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: it is not lowercase hex", ErrHashInvalid)
		}
	}

	return nil
}
