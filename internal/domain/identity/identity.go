// Package identity holds who someone is inside one realm. It imports nothing
// beyond the standard library and uuid.
package identity

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// No "@": a login identifier with one is an email and one without is a
// username, so the two namespaces cannot collide.
var usernamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)

const maxEmailLength = 254

// Private fields with accessors: an identity never moves to another realm, by
// compiler and not by convention.
type Identity struct {
	id        uuid.UUID
	realmID   uuid.UUID
	email     string
	username  string
	status    Status
	createdAt time.Time
	updatedAt time.Time
}

func New(realmID uuid.UUID, email, username string) (*Identity, error) {
	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}

	username, err = NormalizeUsername(username)
	if err != nil {
		return nil, err
	}

	// NewV7 reads the clock and the entropy source; uuid.Must would turn a
	// transient failure into a panic inside a request.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("identity: generating an identifier: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)

	return &Identity{
		id:        id,
		realmID:   realmID,
		email:     email,
		username:  username,
		status:    StatusActive,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// Rehydrate is the repository's door back in. A stored email or username that
// is not in normal form is refused rather than fixed: a lookup by the
// normalised value would never have found that row.
func Rehydrate(
	id, realmID uuid.UUID,
	email, username string,
	status Status,
	createdAt, updatedAt time.Time,
) (*Identity, error) {
	if id == uuid.Nil {
		return nil, ErrIDInvalid
	}

	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if normalized, err := NormalizeEmail(email); err != nil {
		return nil, err
	} else if normalized != email {
		return nil, fmt.Errorf("%w: %q is not normalised", ErrEmailInvalid, email)
	}

	if normalized, err := NormalizeUsername(username); err != nil {
		return nil, err
	} else if normalized != username {
		return nil, fmt.Errorf("%w: %q is not normalised", ErrUsernameInvalid, username)
	}

	if _, err := ParseStatus(string(status)); err != nil {
		return nil, err
	}

	return &Identity{
		id:        id,
		realmID:   realmID,
		email:     email,
		username:  username,
		status:    status,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

func (i *Identity) ID() uuid.UUID        { return i.id }
func (i *Identity) RealmID() uuid.UUID   { return i.realmID }
func (i *Identity) Email() string        { return i.email }
func (i *Identity) Username() string     { return i.username }
func (i *Identity) HasUsername() bool    { return i.username != "" }
func (i *Identity) Status() Status       { return i.status }
func (i *Identity) CreatedAt() time.Time { return i.createdAt }
func (i *Identity) UpdatedAt() time.Time { return i.updatedAt }

func (i *Identity) SetStatus(status Status) error {
	parsed, err := ParseStatus(string(status))
	if err != nil {
		return err
	}

	i.status = parsed
	i.updatedAt = time.Now().UTC().Truncate(time.Microsecond)

	return nil
}

// NormalizeEmail lowercases because the MySQL and MariaDB columns use a binary
// collation: the database compares bytes, so this is what makes uniqueness
// case-insensitive.
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))

	if email == "" || len(email) > maxEmailLength {
		return "", fmt.Errorf("%w: %q", ErrEmailInvalid, raw)
	}

	// ParseAddress also accepts "Name <a@b.c>", comments and quoted local
	// parts; only a bare address comes back equal to what went in.
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || strings.Count(email, "@") != 1 {
		return "", fmt.Errorf("%w: %q", ErrEmailInvalid, raw)
	}

	return email, nil
}

// NormalizeUsername returns "" for an empty input: a username is optional.
func NormalizeUsername(raw string) (string, error) {
	username := strings.ToLower(strings.TrimSpace(raw))
	if username == "" {
		return "", nil
	}

	if !usernamePattern.MatchString(username) {
		return "", fmt.Errorf("%w: %q", ErrUsernameInvalid, raw)
	}

	return username, nil
}
