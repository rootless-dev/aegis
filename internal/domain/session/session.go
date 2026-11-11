// Package session holds the server-side session and the refresh tokens bound
// to it. It imports nothing beyond the standard library and uuid.
//
// Nothing here reads the clock: the caller passes now, so expiry can be tested
// at a chosen instant.
package session

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// The column is varchar(255).
const maxUserAgentLength = 255

// The idle TTL is not a field: it is read from the realm at each check, so a
// realm that shortens it tightens the sessions that already exist.
type Session struct {
	id         uuid.UUID
	realmID    uuid.UUID
	identityID uuid.UUID
	ip         string
	userAgent  string
	createdAt  time.Time
	lastSeenAt time.Time
	expiresAt  time.Time
	endedAt    *time.Time
	endReason  EndReason
}

func New(
	realmID, identityID uuid.UUID,
	now time.Time,
	maxTTL time.Duration,
	ip, userAgent string,
) (*Session, error) {
	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if identityID == uuid.Nil {
		return nil, ErrIdentityInvalid
	}

	if maxTTL <= 0 {
		return nil, ErrTTLInvalid
	}

	ip, err := canonicalIP(ip)
	if err != nil {
		return nil, err
	}

	// NewV7 reads the clock and the entropy source; uuid.Must would turn a
	// transient failure into a panic inside a request.
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("session: generating an identifier: %w", err)
	}

	createdAt := microUTC(now)

	return &Session{
		id:         id,
		realmID:    realmID,
		identityID: identityID,
		ip:         ip,
		userAgent:  sanitizeUserAgent(userAgent),
		createdAt:  createdAt,
		lastSeenAt: createdAt,
		expiresAt:  microUTC(createdAt.Add(maxTTL)),
	}, nil
}

// Rehydrate is the repository's door back in. It refuses what the schema's
// CHECK constraints refuse.
func Rehydrate(
	id, realmID, identityID uuid.UUID,
	ip, userAgent string,
	createdAt, lastSeenAt, expiresAt time.Time,
	endedAt *time.Time,
	endReason EndReason,
) (*Session, error) {
	if id == uuid.Nil {
		return nil, ErrIDInvalid
	}

	if realmID == uuid.Nil {
		return nil, ErrRealmInvalid
	}

	if identityID == uuid.Nil {
		return nil, ErrIdentityInvalid
	}

	ip, err := canonicalIP(ip)
	if err != nil {
		return nil, err
	}

	if len(userAgent) > maxUserAgentLength {
		return nil, fmt.Errorf("%w: %d bytes, the limit is %d", ErrUserAgentInvalid, len(userAgent), maxUserAgentLength)
	}

	if (endedAt == nil) != (endReason == "") {
		return nil, ErrEndInvalid
	}

	if endReason != "" {
		if _, err := ParseEndReason(endReason.String()); err != nil {
			return nil, err
		}
	}

	if !expiresAt.After(createdAt) {
		return nil, ErrTTLInvalid
	}

	if lastSeenAt.Before(createdAt) {
		return nil, ErrLastSeenInvalid
	}

	return &Session{
		id:         id,
		realmID:    realmID,
		identityID: identityID,
		ip:         ip,
		userAgent:  userAgent,
		createdAt:  microUTC(createdAt),
		lastSeenAt: microUTC(lastSeenAt),
		expiresAt:  microUTC(expiresAt),
		endedAt:    microUTCCopy(endedAt),
		endReason:  endReason,
	}, nil
}

func (s *Session) ID() uuid.UUID         { return s.id }
func (s *Session) RealmID() uuid.UUID    { return s.realmID }
func (s *Session) IdentityID() uuid.UUID { return s.identityID }
func (s *Session) IP() string            { return s.ip }
func (s *Session) UserAgent() string     { return s.userAgent }
func (s *Session) CreatedAt() time.Time  { return s.createdAt }
func (s *Session) LastSeenAt() time.Time { return s.lastSeenAt }
func (s *Session) ExpiresAt() time.Time  { return s.expiresAt }
func (s *Session) EndReason() EndReason  { return s.endReason }
func (s *Session) Ended() bool           { return s.endedAt != nil }

// EndedAt returns a copy: a caller holding the pointer could rewrite the
// aggregate's history.
func (s *Session) EndedAt() *time.Time { return microUTCCopy(s.endedAt) }

// IdleDeadline is never past ExpiresAt.
func (s *Session) IdleDeadline(idleTTL time.Duration) time.Time {
	deadline := s.lastSeenAt.Add(idleTTL)
	if deadline.After(s.expiresAt) {
		return s.expiresAt
	}

	return deadline
}

// Usable returns ErrEnded, then ErrExpired, or nil. A deadline is exclusive:
// at exactly that instant the session is already expired.
func (s *Session) Usable(now time.Time, idleTTL time.Duration) error {
	if s.Ended() {
		return fmt.Errorf("%w: %s", ErrEnded, s.endReason)
	}

	if !now.Before(s.expiresAt) {
		return fmt.Errorf("%w: past its absolute deadline", ErrExpired)
	}

	if !now.Before(s.IdleDeadline(idleTTL)) {
		return fmt.Errorf("%w: idle for too long", ErrExpired)
	}

	return nil
}

// Touch never moves backwards: two requests can arrive out of order.
func (s *Session) Touch(now time.Time) {
	if at := microUTC(now); at.After(s.lastSeenAt) {
		s.lastSeenAt = at
	}
}

func (s *Session) End(now time.Time, reason EndReason) error {
	if s.Ended() {
		return ErrEnded
	}

	parsed, err := ParseEndReason(reason.String())
	if err != nil {
		return err
	}

	at := microUTC(now)

	s.endedAt = &at
	s.endReason = parsed

	return nil
}

// The zone is dropped: it names an interface of this host, says nothing about
// the client, and has no length limit while the column does.
func canonicalIP(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return "", ErrIPInvalid
	}

	return addr.Unmap().WithZone("").String(), nil
}

// The user agent is a request header: whatever a client chose to send. Invalid
// UTF-8 goes too, because PostgreSQL refuses the whole row over it.
func sanitizeUserAgent(raw string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}

		return r
	}, strings.ToValidUTF8(raw, ""))

	if len(clean) <= maxUserAgentLength {
		return clean
	}

	cut := maxUserAgentLength
	for cut > 0 && !utf8.RuneStart(clean[cut]) {
		cut--
	}

	return clean[:cut]
}

// Microsecond precision: a value the database cannot hold would read back
// different from what was written.
func microUTC(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func microUTCCopy(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	at := microUTC(*t)

	return &at
}
