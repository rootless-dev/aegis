package session_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
)

const (
	maxTTL  = time.Hour
	idleTTL = 10 * time.Minute
)

var t0 = time.Date(2026, 3, 1, 9, 0, 0, 123456000, time.UTC)

func newID(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generating an identifier: %v", err)
	}

	return id
}

func newSession(t *testing.T) *session.Session {
	t.Helper()

	s, err := session.New(newID(t), newID(t), t0, maxTTL, "192.0.2.1", "Mozilla/5.0")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	return s
}

func isMicrosecondUTC(at time.Time) bool {
	return at.Location() == time.UTC && at.Equal(at.Truncate(time.Microsecond))
}

func TestNewBuildsALiveSession(t *testing.T) {
	realmID, identityID := newID(t), newID(t)

	s, err := session.New(realmID, identityID, t0, maxTTL, "192.0.2.1", "Mozilla/5.0")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if s.ID().Version() != 7 {
		t.Errorf("want a UUIDv7, got version %d", s.ID().Version())
	}

	if s.RealmID() != realmID || s.IdentityID() != identityID {
		t.Error("the realm and the identity must be the ones given")
	}

	if s.IP() != "192.0.2.1" || s.UserAgent() != "Mozilla/5.0" {
		t.Errorf("want the given ip and user agent, got %q and %q", s.IP(), s.UserAgent())
	}

	if !s.CreatedAt().Equal(t0) || !s.LastSeenAt().Equal(t0) {
		t.Errorf("want both stamped at %v, got %v and %v", t0, s.CreatedAt(), s.LastSeenAt())
	}

	if want := t0.Add(maxTTL); !s.ExpiresAt().Equal(want) {
		t.Errorf("expiry: want %v, got %v", want, s.ExpiresAt())
	}

	if s.Ended() || s.EndedAt() != nil || s.EndReason() != "" {
		t.Errorf("a new session must be live, got ended at %v for %q", s.EndedAt(), s.EndReason())
	}
}

func TestNewAcceptsAnUnknownIPAndUserAgent(t *testing.T) {
	s, err := session.New(newID(t), newID(t), t0, maxTTL, "", "")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if s.IP() != "" || s.UserAgent() != "" {
		t.Errorf("want both empty, got %q and %q", s.IP(), s.UserAgent())
	}
}

func TestNewTruncatesToMicrosecondsInUTC(t *testing.T) {
	zone := time.FixedZone("BRT", -3*60*60)
	now := time.Date(2026, 3, 1, 6, 0, 0, 123456789, zone)

	s, err := session.New(newID(t), newID(t), now, maxTTL+789*time.Nanosecond, "", "")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	for name, at := range map[string]time.Time{
		"created": s.CreatedAt(), "last seen": s.LastSeenAt(), "expires": s.ExpiresAt(),
	} {
		if !isMicrosecondUTC(at) {
			t.Errorf("%s: want UTC at microsecond precision, got %v", name, at)
		}
	}

	if want := now.Truncate(time.Microsecond); !s.CreatedAt().Equal(want) {
		t.Errorf("created: want %v, got %v", want, s.CreatedAt())
	}
}

func TestNewRejectsWhatItCannotHold(t *testing.T) {
	realmID, identityID := newID(t), newID(t)

	cases := []struct {
		name       string
		realmID    uuid.UUID
		identityID uuid.UUID
		maxTTL     time.Duration
		ip         string
		want       error
	}{
		{"nil realm", uuid.Nil, identityID, maxTTL, "", session.ErrRealmInvalid},
		{"nil identity", realmID, uuid.Nil, maxTTL, "", session.ErrIdentityInvalid},
		{"zero ttl", realmID, identityID, 0, "", session.ErrTTLInvalid},
		{"negative ttl", realmID, identityID, -time.Second, "", session.ErrTTLInvalid},
		{"garbage ip", realmID, identityID, maxTTL, "not an ip", session.ErrIPInvalid},
	}

	for _, tc := range cases {
		_, err := session.New(tc.realmID, tc.identityID, t0, tc.maxTTL, tc.ip, "")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestTheIPIsStoredInCanonicalForm(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"unknown", "", ""},
		{"ipv4", "192.0.2.1", "192.0.2.1"},
		{"ipv6 expanded and upper case", "2001:0DB8:0000:0000:0000:0000:0000:0001", "2001:db8::1"},
		{"ipv6 loopback", "::1", "::1"},
		{"ipv4 mapped", "::ffff:192.0.2.1", "192.0.2.1"},
		{"ipv4 mapped in hex", "::ffff:c000:201", "192.0.2.1"},
		{"zone", "fe80::1%eth0", "fe80::1"},
		{"zone longer than the column", "fe80::1%" + strings.Repeat("z", 64), "fe80::1"},
		{"longest ipv6", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"},
	}

	for _, tc := range cases {
		s, err := session.New(newID(t), newID(t), t0, maxTTL, tc.raw, "")
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)

			continue
		}

		if s.IP() != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, s.IP())
		}

		if len(s.IP()) > 45 {
			t.Errorf("%s: %d characters do not fit the column", tc.name, len(s.IP()))
		}
	}
}

func TestAnIPThatIsNotAnAddressIsRefused(t *testing.T) {
	malformed := map[string]string{
		"words":           "not an ip",
		"with port":       "192.0.2.1:443",
		"bracketed":       "[::1]",
		"octet too large": "192.0.2.256",
		"leading space":   " 192.0.2.1",
		"cidr":            "192.0.2.0/24",
		"hostname":        "example.com",
		"forwarded list":  "192.0.2.1, 198.51.100.7",
	}

	for name, raw := range malformed {
		if _, err := session.New(newID(t), newID(t), t0, maxTTL, raw, ""); !errors.Is(err, session.ErrIPInvalid) {
			t.Errorf("%s: want ErrIPInvalid from New, got %v", name, err)
		}

		_, err := session.Rehydrate(
			newID(t), newID(t), newID(t), raw, "", t0, t0, t0.Add(maxTTL), nil, "",
		)
		if !errors.Is(err, session.ErrIPInvalid) {
			t.Errorf("%s: want ErrIPInvalid from Rehydrate, got %v", name, err)
		}
	}
}

func TestNewSanitisesTheUserAgent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain", "Mozilla/5.0 (X11; Linux x86_64)", "Mozilla/5.0 (X11; Linux x86_64)"},
		{"multi-byte kept", "navegador/1.0 (ação)", "navegador/1.0 (ação)"},
		{"control characters", "Mozilla\r\n/5.0\x00\t\x1b\x7f", "Mozilla/5.0"},
		{"c1 control", "curl\u0085/8", "curl/8"},
		{"invalid utf-8", "curl\xff/8\xc3", "curl/8"},
		{"at the limit", strings.Repeat("a", 255), strings.Repeat("a", 255)},
		{"one byte over", strings.Repeat("a", 256), strings.Repeat("a", 255)},
		{"two-byte rune across the limit", strings.Repeat("a", 254) + "é", strings.Repeat("a", 254)},
		{"three-byte rune across the limit", strings.Repeat("a", 253) + "€", strings.Repeat("a", 253)},
		{"four-byte rune across the limit", strings.Repeat("a", 252) + "😀", strings.Repeat("a", 252)},
		{"rune ending at the limit", strings.Repeat("a", 253) + "é" + "b", strings.Repeat("a", 253) + "é"},
		{
			"controls dropped before measuring",
			strings.Repeat("a", 100) + "\n" + strings.Repeat("a", 155),
			strings.Repeat("a", 255),
		},
	}

	for _, tc := range cases {
		s, err := session.New(newID(t), newID(t), t0, maxTTL, "", tc.raw)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)

			continue
		}

		if s.UserAgent() != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, s.UserAgent())
		}

		if len(s.UserAgent()) > 255 || !utf8.ValidString(s.UserAgent()) {
			t.Errorf("%s: %d bytes, valid utf-8 %t", tc.name, len(s.UserAgent()), utf8.ValidString(s.UserAgent()))
		}
	}
}

func TestRehydrateRoundTrips(t *testing.T) {
	id, realmID, identityID := newID(t), newID(t), newID(t)
	zone := time.FixedZone("BRT", -3*60*60)
	createdAt := time.Date(2026, 3, 1, 9, 0, 0, 123456000, zone)
	lastSeenAt := createdAt.Add(5 * time.Minute)
	expiresAt := createdAt.Add(maxTTL)
	endedAt := createdAt.Add(20 * time.Minute)

	s, err := session.Rehydrate(
		id, realmID, identityID, "2001:db8::1", "Mozilla/5.0",
		createdAt, lastSeenAt, expiresAt, &endedAt, session.EndReuse,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if s.ID() != id || s.RealmID() != realmID || s.IdentityID() != identityID {
		t.Error("the identifiers must be the stored ones")
	}

	if s.IP() != "2001:db8::1" || s.UserAgent() != "Mozilla/5.0" {
		t.Errorf("want the stored ip and user agent, got %q and %q", s.IP(), s.UserAgent())
	}

	if !s.CreatedAt().Equal(createdAt) || !s.LastSeenAt().Equal(lastSeenAt) || !s.ExpiresAt().Equal(expiresAt) {
		t.Errorf("want the stored instants, got %v, %v and %v", s.CreatedAt(), s.LastSeenAt(), s.ExpiresAt())
	}

	if !s.Ended() || s.EndedAt() == nil || !s.EndedAt().Equal(endedAt) || s.EndReason() != session.EndReuse {
		t.Fatalf("want ended at %v for reuse, got %v for %q", endedAt, s.EndedAt(), s.EndReason())
	}

	for name, at := range map[string]time.Time{
		"created": s.CreatedAt(), "last seen": s.LastSeenAt(), "expires": s.ExpiresAt(), "ended": *s.EndedAt(),
	} {
		if !isMicrosecondUTC(at) {
			t.Errorf("%s: want UTC at microsecond precision, got %v", name, at)
		}
	}
}

func TestRehydrateKeepsALiveSessionLive(t *testing.T) {
	s, err := session.Rehydrate(newID(t), newID(t), newID(t), "", "", t0, t0, t0.Add(maxTTL), nil, "")
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if s.Ended() || s.EndedAt() != nil || s.EndReason() != "" {
		t.Errorf("want a live session, got ended at %v for %q", s.EndedAt(), s.EndReason())
	}
}

func TestRehydrateDoesNotKeepTheCallersPointer(t *testing.T) {
	endedAt := t0.Add(time.Minute)

	s, err := session.Rehydrate(
		newID(t), newID(t), newID(t), "", "", t0, t0, t0.Add(maxTTL), &endedAt, session.EndRevoked,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	endedAt = endedAt.Add(time.Hour)

	if want := t0.Add(time.Minute); !s.EndedAt().Equal(want) {
		t.Errorf("the end moved with the caller's variable: got %v", s.EndedAt())
	}
}

func TestEndedAtDoesNotExposeTheAggregate(t *testing.T) {
	s := newSession(t)

	if err := s.End(t0.Add(time.Minute), session.EndRevoked); err != nil {
		t.Fatalf("ending: %v", err)
	}

	*s.EndedAt() = t0.Add(24 * time.Hour)

	if want := t0.Add(time.Minute); !s.EndedAt().Equal(want) {
		t.Errorf("the end was rewritten through the accessor: got %v", s.EndedAt())
	}
}

// Data a customer's DBA has touched is not trusted input.
func TestRehydrateValidatesWhatItIsGiven(t *testing.T) {
	type row struct {
		id, realmID, identityID          uuid.UUID
		ip, userAgent                    string
		createdAt, lastSeenAt, expiresAt time.Time
		endedAt                          *time.Time
		endReason                        session.EndReason
	}

	endedAt := t0.Add(time.Minute)

	cases := []struct {
		name  string
		spoil func(*row)
		want  error
	}{
		{"nil id", func(r *row) { r.id = uuid.Nil }, session.ErrIDInvalid},
		{"nil realm", func(r *row) { r.realmID = uuid.Nil }, session.ErrRealmInvalid},
		{"nil identity", func(r *row) { r.identityID = uuid.Nil }, session.ErrIdentityInvalid},
		{"garbage ip", func(r *row) { r.ip = "localhost" }, session.ErrIPInvalid},
		{
			"user agent over the column",
			func(r *row) { r.userAgent = strings.Repeat("a", 256) },
			session.ErrUserAgentInvalid,
		},
		{"ended without a reason", func(r *row) { r.endedAt = &endedAt }, session.ErrEndInvalid},
		{"reason without an end", func(r *row) { r.endReason = session.EndRevoked }, session.ErrEndInvalid},
		{
			"unknown reason",
			func(r *row) { r.endedAt, r.endReason = &endedAt, "logout" },
			session.ErrEndReasonInvalid,
		},
		{"expiry at creation", func(r *row) { r.expiresAt = r.createdAt }, session.ErrTTLInvalid},
		{
			"expiry before creation",
			func(r *row) { r.expiresAt = r.createdAt.Add(-time.Microsecond) },
			session.ErrTTLInvalid,
		},
		{
			"last seen before creation",
			func(r *row) { r.lastSeenAt = r.createdAt.Add(-time.Microsecond) },
			session.ErrLastSeenInvalid,
		},
	}

	for _, tc := range cases {
		r := row{
			id: newID(t), realmID: newID(t), identityID: newID(t),
			ip: "192.0.2.1", userAgent: "Mozilla/5.0",
			createdAt: t0, lastSeenAt: t0, expiresAt: t0.Add(maxTTL),
		}
		tc.spoil(&r)

		_, err := session.Rehydrate(
			r.id, r.realmID, r.identityID, r.ip, r.userAgent,
			r.createdAt, r.lastSeenAt, r.expiresAt, r.endedAt, r.endReason,
		)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestRehydrateAcceptsAUserAgentAtTheColumnLimit(t *testing.T) {
	userAgent := strings.Repeat("a", 255)

	s, err := session.Rehydrate(newID(t), newID(t), newID(t), "", userAgent, t0, t0, t0.Add(maxTTL), nil, "")
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if s.UserAgent() != userAgent {
		t.Errorf("the stored user agent must come back untouched, got %d bytes", len(s.UserAgent()))
	}
}

func TestIdleDeadlineIsCappedByTheAbsoluteOne(t *testing.T) {
	s := newSession(t)

	if want := t0.Add(idleTTL); !s.IdleDeadline(idleTTL).Equal(want) {
		t.Errorf("want %v, got %v", want, s.IdleDeadline(idleTTL))
	}

	if got := s.IdleDeadline(2 * maxTTL); !got.Equal(s.ExpiresAt()) {
		t.Errorf("an idle ttl beyond the session's life: want %v, got %v", s.ExpiresAt(), got)
	}

	s.Touch(t0.Add(55 * time.Minute))

	if got := s.IdleDeadline(idleTTL); !got.Equal(s.ExpiresAt()) {
		t.Errorf("seen five minutes before the end: want %v, got %v", s.ExpiresAt(), got)
	}
}

func TestUsableAtEachBoundary(t *testing.T) {
	const tick = time.Microsecond

	cases := []struct {
		name     string
		lastSeen time.Duration
		idleTTL  time.Duration
		at       time.Duration
		want     error
	}{
		{"at creation", 0, idleTTL, 0, nil},
		{"one tick before the idle deadline", 0, idleTTL, idleTTL - tick, nil},
		{"at the idle deadline", 0, idleTTL, idleTTL, session.ErrExpired},
		{"after the idle deadline", 0, idleTTL, idleTTL + time.Minute, session.ErrExpired},
		{"seen again, one tick before the new idle deadline", 30 * time.Minute, idleTTL, 40*time.Minute - tick, nil},
		{"seen again, at the new idle deadline", 30 * time.Minute, idleTTL, 40 * time.Minute, session.ErrExpired},
		{"idle beyond absolute, one tick before the end", 0, 2 * maxTTL, maxTTL - tick, nil},
		{"idle beyond absolute, at the end", 0, 2 * maxTTL, maxTTL, session.ErrExpired},
		{"seen near the end, one tick before the end", 55 * time.Minute, idleTTL, maxTTL - tick, nil},
		{"seen near the end, at the end", 55 * time.Minute, idleTTL, maxTTL, session.ErrExpired},
		{"seen near the end, past the end", 55 * time.Minute, idleTTL, maxTTL + time.Minute, session.ErrExpired},
		{"zero idle ttl", 0, 0, 0, session.ErrExpired},
		{"sub-tick before the idle deadline", 0, idleTTL, idleTTL - time.Nanosecond, nil},
	}

	for _, tc := range cases {
		s, err := session.Rehydrate(
			newID(t), newID(t), newID(t), "", "", t0, t0.Add(tc.lastSeen), t0.Add(maxTTL), nil, "",
		)
		if err != nil {
			t.Fatalf("%s: rehydrating: %v", tc.name, err)
		}

		if err := s.Usable(t0.Add(tc.at), tc.idleTTL); !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestUsableReportsAnEndBeforeAnExpiry(t *testing.T) {
	s := newSession(t)

	if err := s.End(t0.Add(time.Minute), session.EndRevoked); err != nil {
		t.Fatalf("ending: %v", err)
	}

	for name, at := range map[string]time.Time{
		"while it would be valid": t0.Add(2 * time.Minute),
		"past both deadlines":     t0.Add(2 * maxTTL),
	} {
		if err := s.Usable(at, idleTTL); !errors.Is(err, session.ErrEnded) || errors.Is(err, session.ErrExpired) {
			t.Errorf("%s: want ErrEnded alone, got %v", name, err)
		}
	}
}

func TestTouchExtendsTheIdleDeadline(t *testing.T) {
	s := newSession(t)
	zone := time.FixedZone("BRT", -3*60*60)
	seen := t0.Add(5*time.Minute + 789*time.Nanosecond).In(zone)

	s.Touch(seen)

	if want := t0.Add(5 * time.Minute); !s.LastSeenAt().Equal(want) || !isMicrosecondUTC(s.LastSeenAt()) {
		t.Errorf("last seen: want %v in UTC, got %v", want, s.LastSeenAt())
	}

	if err := s.Usable(t0.Add(idleTTL), idleTTL); err != nil {
		t.Errorf("a session seen five minutes ago must be usable, got %v", err)
	}

	if !s.CreatedAt().Equal(t0) || !s.ExpiresAt().Equal(t0.Add(maxTTL)) {
		t.Error("touching must not move the creation or the absolute deadline")
	}
}

func TestTouchNeverMovesBackwards(t *testing.T) {
	s := newSession(t)
	latest := t0.Add(5 * time.Minute)

	s.Touch(latest)

	for name, at := range map[string]time.Time{
		"earlier":         t0.Add(time.Minute),
		"before creation": t0.Add(-time.Hour),
		"same instant":    latest,
		"zero time":       {},
	} {
		s.Touch(at)

		if !s.LastSeenAt().Equal(latest) {
			t.Errorf("%s: last seen moved to %v", name, s.LastSeenAt())
		}
	}
}

func TestEndStampsTheInstantAndTheReason(t *testing.T) {
	for _, reason := range []session.EndReason{session.EndRevoked, session.EndReuse, session.EndRealmDisabled} {
		s := newSession(t)
		zone := time.FixedZone("BRT", -3*60*60)
		now := t0.Add(time.Minute + 789*time.Nanosecond).In(zone)

		if err := s.End(now, reason); err != nil {
			t.Fatalf("%s: %v", reason, err)
		}

		if !s.Ended() || s.EndReason() != reason {
			t.Errorf("%s: want ended for it, got %t for %q", reason, s.Ended(), s.EndReason())
		}

		if want := t0.Add(time.Minute); !s.EndedAt().Equal(want) || !isMicrosecondUTC(*s.EndedAt()) {
			t.Errorf("%s: want %v in UTC, got %v", reason, want, s.EndedAt())
		}
	}
}

func TestEndTwiceKeepsTheFirstEnd(t *testing.T) {
	s := newSession(t)

	if err := s.End(t0.Add(time.Minute), session.EndReuse); err != nil {
		t.Fatalf("ending: %v", err)
	}

	if err := s.End(t0.Add(time.Hour), session.EndRevoked); !errors.Is(err, session.ErrEnded) {
		t.Errorf("want ErrEnded, got %v", err)
	}

	if s.EndReason() != session.EndReuse || !s.EndedAt().Equal(t0.Add(time.Minute)) {
		t.Errorf("the first end was rewritten: %v for %q", s.EndedAt(), s.EndReason())
	}
}

func TestEndRefusesAnUnknownReasonAndStaysLive(t *testing.T) {
	for _, reason := range []session.EndReason{"", "logout", "REVOKED"} {
		s := newSession(t)

		if err := s.End(t0.Add(time.Minute), reason); !errors.Is(err, session.ErrEndReasonInvalid) {
			t.Errorf("%q: want ErrEndReasonInvalid, got %v", reason, err)
		}

		if s.Ended() || s.EndedAt() != nil || s.EndReason() != "" {
			t.Errorf("%q: a refused end must leave the session live", reason)
		}
	}
}

func TestParseEndReasonRoundTripsTheThreeValues(t *testing.T) {
	for _, want := range []session.EndReason{session.EndRevoked, session.EndReuse, session.EndRealmDisabled} {
		got, err := session.ParseEndReason(want.String())
		if err != nil || got != want {
			t.Errorf("want %q, got %q and %v", want, got, err)
		}
	}

	for _, raw := range []string{"", "expired", "Revoked", " reuse"} {
		if _, err := session.ParseEndReason(raw); !errors.Is(err, session.ErrEndReasonInvalid) {
			t.Errorf("%q: want ErrEndReasonInvalid, got %v", raw, err)
		}
	}
}
