// Package credential holds how an identity proves who it is. It imports
// nothing beyond the standard library and uuid.
package credential

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// The column is varchar(255).
const maxHashLength = 255

// mustChange lives here and not on the identity: it is the proof that is
// compromised, not the person.
type Credential struct {
	id         uuid.UUID
	realmID    uuid.UUID
	identityID uuid.UUID
	typ        Type
	hash       string
	mustChange bool
	lastUsedAt *time.Time
	createdAt  time.Time
	updatedAt  time.Time
}

// NewPassword takes a PHC string another package produced. No plaintext
// enters this package.
func NewPassword(realmID, identityID uuid.UUID, hash string, mustChange bool) (*Credential, error) {
	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if identityID == uuid.Nil {
		return nil, ErrIdentityInvalid
	}

	if err := validateHash(hash); err != nil {
		return nil, err
	}

	// NewV7 reads the clock and the entropy source; uuid.Must would turn a
	// transient failure into a panic inside a request.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("credential: generating an identifier: %w", err)
	}

	now := nowUTC()

	return &Credential{
		id:         id,
		realmID:    realmID,
		identityID: identityID,
		typ:        TypePassword,
		hash:       hash,
		mustChange: mustChange,
		createdAt:  now,
		updatedAt:  now,
	}, nil
}

// Rehydrate is the repository's door back in.
func Rehydrate(
	id, realmID, identityID uuid.UUID,
	typ Type,
	hash string,
	mustChange bool,
	lastUsedAt *time.Time,
	createdAt, updatedAt time.Time,
) (*Credential, error) {
	if id == uuid.Nil {
		return nil, ErrIDInvalid
	}

	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if identityID == uuid.Nil {
		return nil, ErrIdentityInvalid
	}

	if _, err := ParseType(typ.String()); err != nil {
		return nil, err
	}

	if err := validateHash(hash); err != nil {
		return nil, err
	}

	return &Credential{
		id:         id,
		realmID:    realmID,
		identityID: identityID,
		typ:        typ,
		hash:       hash,
		mustChange: mustChange,
		lastUsedAt: utcCopy(lastUsedAt),
		createdAt:  createdAt.UTC(),
		updatedAt:  updatedAt.UTC(),
	}, nil
}

func (c *Credential) ID() uuid.UUID         { return c.id }
func (c *Credential) RealmID() uuid.UUID    { return c.realmID }
func (c *Credential) IdentityID() uuid.UUID { return c.identityID }
func (c *Credential) Type() Type            { return c.typ }
func (c *Credential) Hash() string          { return c.hash }
func (c *Credential) MustChange() bool      { return c.mustChange }
func (c *Credential) CreatedAt() time.Time  { return c.createdAt }
func (c *Credential) UpdatedAt() time.Time  { return c.updatedAt }

// LastUsedAt is nil until the first successful use. It returns a copy: a
// caller holding the pointer could rewrite the aggregate's history.
func (c *Credential) LastUsedAt() *time.Time { return utcCopy(c.lastUsedAt) }

// Replace is a new secret: someone set a password. It decides must-change.
func (c *Credential) Replace(hash string, mustChange bool) error {
	if err := validateHash(hash); err != nil {
		return err
	}

	c.hash = hash
	c.mustChange = mustChange
	c.updatedAt = nowUTC()

	return nil
}

// Rehash is the same secret under stronger parameters. It keeps must-change.
func (c *Credential) Rehash(hash string) error {
	if err := validateHash(hash); err != nil {
		return err
	}

	c.hash = hash
	c.updatedAt = nowUTC()

	return nil
}

// MarkUsed records a successful verification.
func (c *Credential) MarkUsed() {
	now := nowUTC()

	c.lastUsedAt = &now
	c.updatedAt = now
}

// Form only. Which algorithms and parameters are acceptable is the hasher's
// business. The messages never quote the value: a hash must not reach a log.
func validateHash(hash string) error {
	if hash == "" {
		return fmt.Errorf("%w: it is empty", ErrHashInvalid)
	}

	if len(hash) > maxHashLength {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrHashInvalid, len(hash), maxHashLength)
	}

	if !strings.HasPrefix(hash, "$") {
		return fmt.Errorf("%w: it is not in PHC format", ErrHashInvalid)
	}

	if strings.ContainsFunc(hash, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("%w: it carries whitespace or a control character", ErrHashInvalid)
	}

	return nil
}

func utcCopy(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

// Microsecond precision: a value the database cannot hold would read back
// different from what was written.
func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
