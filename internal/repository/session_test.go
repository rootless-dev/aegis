package repository_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

func TestSessionRoundTripsEveryCombinationOfItsNullableColumns(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertSessionRoundTrips(t, db, store)
}

// Shared with integration_test.go. Every combination, because each nullable
// column is mapped by hand in both directions.
func assertSessionRoundTrips(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()
	realmID := seedRealm(t, store, "session-roundtrip")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	createdAt := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	lastSeenAt := time.Date(2026, 3, 4, 5, 6, 8, 234567000, time.UTC)
	expiresAt := time.Date(2026, 3, 4, 13, 6, 7, 345678000, time.UTC)
	endedAt := time.Date(2026, 3, 4, 5, 6, 9, 987654000, time.UTC)

	for _, ip := range []string{"", "2001:db8::7"} {
		for _, userAgent := range []string{"", "Mozilla/5.0 (X11; Linux x86_64)"} {
			for _, ended := range []bool{false, true} {
				var (
					wantEndedAt *time.Time
					wantReason  session.EndReason
				)

				if ended {
					wantEndedAt, wantReason = &endedAt, session.EndReuse
				}

				want, err := session.Rehydrate(
					newID(t), realmID, owner.ID(), ip, userAgent, createdAt, lastSeenAt, expiresAt, wantEndedAt, wantReason,
				)
				if err != nil {
					t.Fatalf("building: %v", err)
				}

				if err := store.Sessions().Create(ctx, want); err != nil {
					t.Fatalf("creating with ip %q, user agent %q, ended %v: %v", ip, userAgent, ended, err)
				}

				got, err := store.Sessions().Find(ctx, realmID, want.ID())
				if err != nil {
					t.Fatalf("reading back: %v", err)
				}

				if got.ID() != want.ID() || got.RealmID() != realmID || got.IdentityID() != owner.ID() {
					t.Errorf("got %s in realm %s for identity %s", got.ID(), got.RealmID(), got.IdentityID())
				}

				if got.IP() != ip || got.UserAgent() != userAgent || got.EndReason() != wantReason {
					t.Errorf("got ip %q, user agent %q, end reason %q", got.IP(), got.UserAgent(), got.EndReason())
				}

				if !got.CreatedAt().Equal(createdAt) || !got.LastSeenAt().Equal(lastSeenAt) ||
					!got.ExpiresAt().Equal(expiresAt) || got.CreatedAt().Location() != time.UTC {
					t.Errorf("timestamps: got %s, %s and %s", got.CreatedAt(), got.LastSeenAt(), got.ExpiresAt())
				}

				if ended != got.Ended() || (ended && !got.EndedAt().Equal(endedAt)) {
					t.Errorf("ended_at: want ended %v at %s, got %v", ended, endedAt, got.EndedAt())
				}

				for column, wantNull := range map[string]bool{
					"ip": ip == "", "user_agent": userAgent == "", "ended_at": !ended, "end_reason": !ended,
				} {
					if isNull(t, db, "sessions", column, want.ID()) != wantNull {
						t.Errorf("%s: want NULL %v with ip %q, user agent %q, ended %v",
							column, wantNull, ip, userAgent, ended)
					}
				}
			}
		}
	}
}

func TestSessionFindIsScopedToTheRealm(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	created := seedSession(t, store, seedIdentity(t, store, home, "ada@example.com", ""))

	if _, err := store.Sessions().Find(ctx, other, created.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("another realm: want session.ErrNotFound, got %v", err)
	}

	if _, err := store.Sessions().Find(ctx, home, newID(t)); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("a missing id: want session.ErrNotFound, got %v", err)
	}
}

func TestSessionListByIdentityIsNewestFirstAndLimited(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	seedSession(t, store, seedIdentity(t, store, home, "grace@example.com", ""))
	seedSession(t, store, seedIdentity(t, store, other, "ada@example.com", ""))

	oldest := seedSession(t, store, owner)
	ended := seedEndedSession(t, store, owner)
	newest := seedSession(t, store, owner)

	all, err := store.Sessions().ListByIdentity(ctx, home, owner.ID(), 10)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(all) != 3 || all[0].ID() != newest.ID() || all[1].ID() != ended.ID() || all[2].ID() != oldest.ID() {
		t.Fatalf("want the identity's three sessions newest first, ended included, got %d", len(all))
	}

	limited, err := store.Sessions().ListByIdentity(ctx, home, owner.ID(), 2)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(limited) != 2 || limited[0].ID() != newest.ID() || limited[1].ID() != ended.ID() {
		t.Errorf("want the two newest, got %d", len(limited))
	}

	foreign, err := store.Sessions().ListByIdentity(ctx, other, owner.ID(), 10)
	if err != nil || len(foreign) != 0 {
		t.Errorf("want nothing through another realm, got %d, %v", len(foreign), err)
	}
}

func TestSessionListByIdentityRefusesANonPositiveLimit(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	for _, limit := range []int{0, -1} {
		_, err := store.Sessions().ListByIdentity(context.Background(), realmID, owner.ID(), limit)
		if !errors.Is(err, service.ErrLimitNotPositive) {
			t.Errorf("limit %d: want service.ErrLimitNotPositive, got %v", limit, err)
		}
	}
}

func TestSessionTouchWritesOnlyLastSeenAt(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")
	// A real row, so that a write to identity_id would pass the foreign key.
	stranger := seedIdentity(t, store, realmID, "grace@example.com", "")

	created := seedSession(t, store, owner)
	before := rawRow(t, db, "sessions", created.ID())
	seenAt := created.LastSeenAt().Add(90*time.Second + 123456*time.Microsecond)

	diverging, err := session.Rehydrate(
		created.ID(), realmID, stranger.ID(), "198.51.100.9", "other",
		created.CreatedAt().Add(-time.Hour), seenAt, created.ExpiresAt().Add(time.Hour), nil, "",
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Sessions().Touch(ctx, diverging); err != nil {
		t.Fatalf("touching: %v", err)
	}

	after := rawRow(t, db, "sessions", created.ID())

	for column := range before {
		if column != "last_seen_at" && fmt.Sprint(before[column]) != fmt.Sprint(after[column]) {
			t.Errorf("%s must be untouched by Touch: %v became %v", column, before[column], after[column])
		}
	}

	got, err := store.Sessions().Find(ctx, realmID, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !got.LastSeenAt().Equal(seenAt) {
		t.Errorf("last_seen_at: want %s, got %s", seenAt, got.LastSeenAt())
	}
}

func TestSessionTouchLeavesAnEndedSessionAlone(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	ended := seedEndedSession(t, store, seedIdentity(t, store, realmID, "ada@example.com", ""))

	// A stale copy from before the end, the only way a caller gets here.
	stale, err := session.Rehydrate(
		ended.ID(), realmID, ended.IdentityID(), ended.IP(), ended.UserAgent(),
		ended.CreatedAt(), ended.LastSeenAt().Add(time.Minute), ended.ExpiresAt(), nil, "",
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Sessions().Touch(ctx, stale); err != nil {
		t.Fatalf("want no error touching an ended session, got %v", err)
	}

	got, err := store.Sessions().Find(ctx, realmID, ended.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !got.LastSeenAt().Equal(ended.LastSeenAt()) {
		t.Errorf("last_seen_at moved on an ended session: %s", got.LastSeenAt())
	}
}

func TestSessionTouchOfAMissingOrForeignSessionWritesNothing(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	if err := store.Sessions().Touch(ctx, makeSession(t, owner)); err != nil {
		t.Errorf("a missing session: want no error, got %v", err)
	}

	created := seedSession(t, store, owner)

	foreign, err := session.Rehydrate(
		created.ID(), other, owner.ID(), "", "",
		created.CreatedAt(), created.LastSeenAt().Add(time.Minute), created.ExpiresAt(), nil, "",
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Sessions().Touch(ctx, foreign); err != nil {
		t.Errorf("another realm: want no error, got %v", err)
	}

	got, err := store.Sessions().Find(ctx, home, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !got.LastSeenAt().Equal(created.LastSeenAt()) {
		t.Errorf("the touch crossed realms: %s", got.LastSeenAt())
	}
}

func TestSessionEndWritesOnceAndOnlyItsTwoColumns(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")
	stranger := seedIdentity(t, store, realmID, "grace@example.com", "")

	created := seedSession(t, store, owner)
	before := rawRow(t, db, "sessions", created.ID())
	endedAt := created.CreatedAt().Add(5*time.Minute + 654321*time.Microsecond)

	diverging, err := session.Rehydrate(
		created.ID(), realmID, stranger.ID(), "198.51.100.9", "other",
		created.CreatedAt().Add(-time.Hour), created.LastSeenAt().Add(time.Minute),
		created.ExpiresAt().Add(time.Hour), &endedAt, session.EndRevoked,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	ended, err := store.Sessions().End(ctx, diverging)
	if err != nil || !ended {
		t.Fatalf("want the end written, got %v, %v", ended, err)
	}

	after := rawRow(t, db, "sessions", created.ID())

	for column := range before {
		if column != "ended_at" && column != "end_reason" && fmt.Sprint(before[column]) != fmt.Sprint(after[column]) {
			t.Errorf("%s must be untouched by End: %v became %v", column, before[column], after[column])
		}
	}

	later := endedAt.Add(time.Hour)

	again, err := session.Rehydrate(
		created.ID(), realmID, owner.ID(), created.IP(), created.UserAgent(),
		created.CreatedAt(), created.LastSeenAt(), created.ExpiresAt(), &later, session.EndReuse,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	ended, err = store.Sessions().End(ctx, again)
	if err != nil || ended {
		t.Fatalf("want the second end refused without an error, got %v, %v", ended, err)
	}

	got, err := store.Sessions().Find(ctx, realmID, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !got.Ended() || !got.EndedAt().Equal(endedAt) || got.EndReason() != session.EndRevoked {
		t.Errorf("want the first end kept, got %v for %q", got.EndedAt(), got.EndReason())
	}
}

func TestSessionEndOfAMissingOrForeignSessionWritesNothing(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	never := makeSession(t, owner)
	if err := never.End(time.Now(), session.EndRevoked); err != nil {
		t.Fatalf("ending: %v", err)
	}

	if ended, err := store.Sessions().End(ctx, never); err != nil || ended {
		t.Errorf("a missing session: want false without an error, got %v, %v", ended, err)
	}

	created := seedSession(t, store, owner)
	endedAt := created.CreatedAt().Add(time.Minute)

	foreign, err := session.Rehydrate(
		created.ID(), other, owner.ID(), "", "",
		created.CreatedAt(), created.LastSeenAt(), created.ExpiresAt(), &endedAt, session.EndRevoked,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if ended, err := store.Sessions().End(ctx, foreign); err != nil || ended {
		t.Errorf("another realm: want false without an error, got %v, %v", ended, err)
	}

	got, err := store.Sessions().Find(ctx, home, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Ended() {
		t.Error("the end crossed realms")
	}
}

// Without the guard, Postgres and SQLite would report a NULL-over-NULL write
// as a session ended.
func TestSessionEndRefusesAnAggregateThatWasNotEnded(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")
	created := seedSession(t, store, seedIdentity(t, store, realmID, "ada@example.com", ""))

	ended, err := store.Sessions().End(context.Background(), created)
	if !errors.Is(err, session.ErrEndInvalid) || ended {
		t.Errorf("want session.ErrEndInvalid and false, got %v, %v", ended, err)
	}
}

func TestSessionEndAllCountsOnlyTheLiveSessionsOfItsScope(t *testing.T) {
	assertEndAllCounts(t, newStore(t))
}

// Shared with integration_test.go: RowsAffected is where the engines differ.
func assertEndAllCounts(t *testing.T, store service.Store) {
	t.Helper()

	ctx := context.Background()
	home := seedRealm(t, store, "endall-home")
	other := seedRealm(t, store, "endall-other")

	ada := seedIdentity(t, store, home, "ada@example.com", "")
	grace := seedIdentity(t, store, home, "grace@example.com", "")
	outsider := seedIdentity(t, store, other, "ada@example.com", "")

	adaLive := []*session.Session{seedSession(t, store, ada), seedSession(t, store, ada)}
	adaEnded := seedEndedSession(t, store, ada)
	graceLive := []*session.Session{seedSession(t, store, grace), seedSession(t, store, grace)}
	graceEnded := seedEndedSession(t, store, grace)
	outsiderLive := []*session.Session{seedSession(t, store, outsider), seedSession(t, store, outsider)}

	at := time.Date(2030, 1, 2, 3, 4, 5, 678901000, time.UTC)

	assertEndAll := func(step string, want int64, got int64, err error) {
		t.Helper()

		if err != nil || got != want {
			t.Errorf("%s: want %d ended, got %d, %v", step, want, got, err)
		}
	}

	assertState := func(step string, realmID uuid.UUID, sessions []*session.Session, wantEnded bool) {
		t.Helper()

		for _, s := range sessions {
			got, err := store.Sessions().Find(ctx, realmID, s.ID())
			if err != nil {
				t.Fatalf("%s: reading back: %v", step, err)
			}

			if got.Ended() != wantEnded {
				t.Errorf("%s: session %s: want ended %v, got %v", step, s.ID(), wantEnded, got.Ended())

				continue
			}

			if wantEnded && (!got.EndedAt().Equal(at) || got.EndReason() != session.EndRealmDisabled) {
				t.Errorf("%s: session %s: got %s for %q", step, s.ID(), got.EndedAt(), got.EndReason())
			}
		}
	}

	count, err := store.Sessions().EndAllByIdentity(ctx, other, ada.ID(), at, session.EndRealmDisabled)
	assertEndAll("an identity through another realm", 0, count, err)

	count, err = store.Sessions().EndAllByIdentity(ctx, home, ada.ID(), at, session.EndRealmDisabled)
	assertEndAll("by identity", 2, count, err)
	assertState("by identity", home, adaLive, true)
	assertState("by identity", home, graceLive, false)
	assertState("by identity", other, outsiderLive, false)

	count, err = store.Sessions().EndAllByIdentity(ctx, home, ada.ID(), at.Add(time.Hour), session.EndRevoked)
	assertEndAll("by identity again", 0, count, err)
	assertState("by identity again", home, adaLive, true)

	count, err = store.Sessions().EndAllByRealm(ctx, home, at, session.EndRealmDisabled)
	assertEndAll("by realm", 2, count, err)
	assertState("by realm", home, graceLive, true)
	assertState("by realm", other, outsiderLive, false)

	count, err = store.Sessions().EndAllByRealm(ctx, home, at.Add(time.Hour), session.EndRevoked)
	assertEndAll("by realm again", 0, count, err)

	// The ones ended before any of this keep their own end.
	for _, earlier := range []*session.Session{adaEnded, graceEnded} {
		got, err := store.Sessions().Find(ctx, home, earlier.ID())
		if err != nil {
			t.Fatalf("reading back: %v", err)
		}

		if !got.EndedAt().Equal(*earlier.EndedAt()) || got.EndReason() != earlier.EndReason() {
			t.Errorf("an earlier end was overwritten: %s for %q", got.EndedAt(), got.EndReason())
		}
	}

	count, err = store.Sessions().EndAllByRealm(ctx, other, at, session.EndRealmDisabled)
	assertEndAll("the other realm", 2, count, err)
}

func TestDeletingAnIdentityOrARealmRemovesTheSessions(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	kept := seedIdentity(t, store, realmID, "ada@example.com", "")
	removed := seedIdentity(t, store, realmID, "grace@example.com", "")

	keptSession := seedSession(t, store, kept)
	removedSession := seedSession(t, store, removed)

	if err := db.Exec("DELETE FROM identities WHERE id = ?", removed.ID().String()).Error; err != nil {
		t.Fatalf("deleting the identity: %v", err)
	}

	if _, err := store.Sessions().Find(ctx, realmID, removedSession.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("want the session gone with its identity, got %v", err)
	}

	if _, err := store.Sessions().Find(ctx, realmID, keptSession.ID()); err != nil {
		t.Errorf("the other identity's session must survive: %v", err)
	}

	if err := db.Exec("DELETE FROM realms WHERE id = ?", realmID.String()).Error; err != nil {
		t.Fatalf("deleting the realm: %v", err)
	}

	if _, err := store.Sessions().Find(ctx, realmID, keptSession.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("want the session gone with the realm, got %v", err)
	}
}

func TestInTxRollsBackASessionAndItsRefreshTokenTogether(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	created := makeSession(t, owner)
	token := makeRefreshToken(t, created, uuid.Nil)
	sentinel := errors.New("stop")

	err := store.InTx(ctx, func(tx service.Store) error {
		if err := tx.Sessions().Create(ctx, created); err != nil {
			return err
		}

		if err := tx.RefreshTokens().Create(ctx, token); err != nil {
			return err
		}

		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("the callback error must reach the caller, got %v", err)
	}

	if _, err := store.Sessions().Find(ctx, realmID, created.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("the session survived a rollback: %v", err)
	}

	_, err = store.RefreshTokens().FindByHash(ctx, realmID, token.Hash())
	if !errors.Is(err, session.ErrTokenNotFound) {
		t.Errorf("the refresh token survived a rollback: %v", err)
	}
}

func makeSession(t *testing.T, owner *identity.Identity) *session.Session {
	t.Helper()

	built, err := session.New(owner.RealmID(), owner.ID(), time.Now(), time.Hour, "203.0.113.7", "curl/8.7.1")
	if err != nil {
		t.Fatalf("building a session: %v", err)
	}

	return built
}

func seedSession(t *testing.T, store service.Store, owner *identity.Identity) *session.Session {
	t.Helper()

	created := makeSession(t, owner)

	if err := store.Sessions().Create(context.Background(), created); err != nil {
		t.Fatalf("creating a session: %v", err)
	}

	return created
}

func seedEndedSession(t *testing.T, store service.Store, owner *identity.Identity) *session.Session {
	t.Helper()

	created := makeSession(t, owner)

	if err := created.End(created.CreatedAt().Add(time.Second), session.EndReuse); err != nil {
		t.Fatalf("ending a session: %v", err)
	}

	if err := store.Sessions().Create(context.Background(), created); err != nil {
		t.Fatalf("creating an ended session: %v", err)
	}

	return created
}

func newID(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	return id
}

// isNull asks the engine rather than the driver, whose NULL is not the same Go
// value on all four.
func isNull(t *testing.T, db *gorm.DB, table, column string, id uuid.UUID) bool {
	t.Helper()

	var count int64

	err := db.Raw("SELECT COUNT(*) FROM "+table+" WHERE id = ? AND "+column+" IS NULL", id.String()).Scan(&count).Error
	if err != nil {
		t.Fatalf("reading %s.%s: %v", table, column, err)
	}

	return count == 1
}
