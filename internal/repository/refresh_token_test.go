package repository_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

func TestRefreshTokenRoundTripsEveryCombinationOfItsNullableColumns(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertRefreshTokenRoundTrips(t, db, store)
}

// Shared with integration_test.go.
func assertRefreshTokenRoundTrips(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()
	realmID := seedRealm(t, store, "token-roundtrip")
	owner := seedSession(t, store, seedIdentity(t, store, realmID, "ada@example.com", ""))
	root := seedRefreshToken(t, store, owner, uuid.Nil)

	createdAt := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	expiresAt := time.Date(2026, 3, 4, 13, 6, 7, 345678000, time.UTC)
	usedAt := time.Date(2026, 3, 4, 5, 6, 9, 987654000, time.UTC)

	for _, parentID := range []uuid.UUID{uuid.Nil, root.ID()} {
		for _, used := range []bool{false, true} {
			var wantUsedAt *time.Time
			if used {
				wantUsedAt = &usedAt
			}

			hash := session.HashSecret(fmt.Sprint(parentID, used))

			want, err := session.RehydrateRefreshToken(
				newID(t), realmID, owner.ID(), parentID, hash, createdAt, expiresAt, wantUsedAt,
			)
			if err != nil {
				t.Fatalf("building: %v", err)
			}

			if err := store.RefreshTokens().Create(ctx, want); err != nil {
				t.Fatalf("creating with parent %s, used %v: %v", parentID, used, err)
			}

			got, err := store.RefreshTokens().FindByHash(ctx, realmID, hash)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}

			if got.ID() != want.ID() || got.RealmID() != realmID || got.SessionID() != owner.ID() {
				t.Errorf("got %s in realm %s for session %s", got.ID(), got.RealmID(), got.SessionID())
			}

			if got.ParentID() != parentID || got.Hash() != hash {
				t.Errorf("got parent %s and a hash of %d characters", got.ParentID(), len(got.Hash()))
			}

			if !got.CreatedAt().Equal(createdAt) || !got.ExpiresAt().Equal(expiresAt) ||
				got.CreatedAt().Location() != time.UTC {
				t.Errorf("timestamps: got %s and %s", got.CreatedAt(), got.ExpiresAt())
			}

			if used != got.Used() || (used && !got.UsedAt().Equal(usedAt)) {
				t.Errorf("used_at: want used %v at %s, got %v", used, usedAt, got.UsedAt())
			}

			if isNull(t, db, "refresh_tokens", "parent_id", want.ID()) != (parentID == uuid.Nil) {
				t.Errorf("parent_id: wrong NULL-ness with parent %s", parentID)
			}

			if isNull(t, db, "refresh_tokens", "used_at", want.ID()) == used {
				t.Errorf("used_at: wrong NULL-ness with used %v", used)
			}
		}
	}
}

func TestRefreshTokenFindByHashIsScopedToTheRealm(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	owner := seedSession(t, store, seedIdentity(t, store, home, "ada@example.com", ""))
	token := seedRefreshToken(t, store, owner, uuid.Nil)

	if _, err := store.RefreshTokens().FindByHash(ctx, other, token.Hash()); !errors.Is(err, session.ErrTokenNotFound) {
		t.Errorf("another realm: want session.ErrTokenNotFound, got %v", err)
	}

	_, err := store.RefreshTokens().FindByHash(ctx, home, session.HashSecret("never issued"))
	if !errors.Is(err, session.ErrTokenNotFound) {
		t.Errorf("an unknown hash: want session.ErrTokenNotFound, got %v", err)
	}
}

func TestRefreshTokenRefusesADuplicateHash(t *testing.T) {
	assertDuplicateHashIsRefused(t, newStore(t))
}

// Shared with integration_test.go. The index is not scoped to the realm, so
// the second realm is refused as well.
func assertDuplicateHashIsRefused(t *testing.T, store service.Store) {
	t.Helper()

	ctx := context.Background()
	home := seedRealm(t, store, "hash-home")
	other := seedRealm(t, store, "hash-other")
	owner := seedSession(t, store, seedIdentity(t, store, home, "ada@example.com", ""))
	outsider := seedSession(t, store, seedIdentity(t, store, other, "ada@example.com", ""))
	first := seedRefreshToken(t, store, owner, uuid.Nil)

	for _, target := range []*session.Session{owner, outsider} {
		duplicate, err := session.NewRefreshToken(
			target.RealmID(), target.ID(), uuid.Nil, first.Hash(), time.Now(), time.Now().Add(time.Hour),
		)
		if err != nil {
			t.Fatalf("building: %v", err)
		}

		if err := store.RefreshTokens().Create(ctx, duplicate); !errors.Is(err, session.ErrHashInvalid) {
			t.Errorf("realm %s: want session.ErrHashInvalid, got %v", target.RealmID(), err)
		}
	}
}

func TestRefreshTokenConsumeWritesOnceAndOnlyUsedAt(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedSession(t, store, seedIdentity(t, store, realmID, "ada@example.com", ""))
	root := seedRefreshToken(t, store, owner, uuid.Nil)
	token := seedRefreshToken(t, store, owner, uuid.Nil)

	before := rawRow(t, db, "refresh_tokens", token.ID())
	usedAt := token.CreatedAt().Add(5*time.Minute + 654321*time.Microsecond)

	diverging, err := session.RehydrateRefreshToken(
		token.ID(), realmID, owner.ID(), root.ID(), session.HashSecret("another"),
		token.CreatedAt().Add(-time.Hour), token.ExpiresAt().Add(time.Hour), &usedAt,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	consumed, err := store.RefreshTokens().Consume(ctx, diverging)
	if err != nil || !consumed {
		t.Fatalf("want the token consumed, got %v, %v", consumed, err)
	}

	after := rawRow(t, db, "refresh_tokens", token.ID())

	for column := range before {
		if column != "used_at" && fmt.Sprint(before[column]) != fmt.Sprint(after[column]) {
			t.Errorf("%s must be untouched by Consume: %v became %v", column, before[column], after[column])
		}
	}

	later := usedAt.Add(time.Hour)

	again, err := session.RehydrateRefreshToken(
		token.ID(), realmID, owner.ID(), uuid.Nil, token.Hash(), token.CreatedAt(), token.ExpiresAt(), &later,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	consumed, err = store.RefreshTokens().Consume(ctx, again)
	if err != nil || consumed {
		t.Fatalf("want the second consume refused without an error, got %v, %v", consumed, err)
	}

	got, err := store.RefreshTokens().FindByHash(ctx, realmID, token.Hash())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !got.Used() || !got.UsedAt().Equal(usedAt) {
		t.Errorf("want the first use kept at %s, got %v", usedAt, got.UsedAt())
	}
}

func TestRefreshTokenConsumeOfAMissingOrForeignTokenWritesNothing(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	owner := seedSession(t, store, seedIdentity(t, store, home, "ada@example.com", ""))

	never := makeRefreshToken(t, owner, uuid.Nil)
	if err := never.Use(time.Now()); err != nil {
		t.Fatalf("using: %v", err)
	}

	if consumed, err := store.RefreshTokens().Consume(ctx, never); err != nil || consumed {
		t.Errorf("a missing token: want false without an error, got %v, %v", consumed, err)
	}

	token := seedRefreshToken(t, store, owner, uuid.Nil)
	usedAt := token.CreatedAt().Add(time.Minute)

	foreign, err := session.RehydrateRefreshToken(
		token.ID(), other, owner.ID(), uuid.Nil, token.Hash(), token.CreatedAt(), token.ExpiresAt(), &usedAt,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if consumed, err := store.RefreshTokens().Consume(ctx, foreign); err != nil || consumed {
		t.Errorf("another realm: want false without an error, got %v, %v", consumed, err)
	}

	got, err := store.RefreshTokens().FindByHash(ctx, home, token.Hash())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Used() {
		t.Error("the consume crossed realms")
	}
}

// Without the guard, Postgres and SQLite would report a NULL-over-NULL write
// as a token consumed.
func TestRefreshTokenConsumeRefusesAnAggregateThatWasNotUsed(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")
	owner := seedSession(t, store, seedIdentity(t, store, realmID, "ada@example.com", ""))
	token := seedRefreshToken(t, store, owner, uuid.Nil)

	consumed, err := store.RefreshTokens().Consume(context.Background(), token)
	if err == nil || consumed {
		t.Errorf("want an error and false, got %v, %v", consumed, err)
	}
}

func TestRefreshTokenConcurrentConsumeHasExactlyOneWinner(t *testing.T) {
	db, store := newStoreAndDB(t)

	// database.Open pins SQLite to one connection; newStoreAndDB does not, and
	// several writers on this file would fail with SQLITE_BUSY instead of racing.
	pool, err := db.DB()
	if err != nil {
		t.Fatalf("reaching the pool: %v", err)
	}

	pool.SetMaxOpenConns(1)

	assertConcurrentConsumeHasOneWinner(t, store)
}

// Shared with integration_test.go, where the contenders hold real concurrent
// transactions.
func assertConcurrentConsumeHasOneWinner(t *testing.T, store service.Store) {
	t.Helper()

	const contenders = 16

	ctx := context.Background()
	realmID := seedRealm(t, store, "consume-race")
	owner := seedSession(t, store, seedIdentity(t, store, realmID, "ada@example.com", ""))
	token := seedRefreshToken(t, store, owner, uuid.Nil)

	// Every contender holds a copy read while the token was unused, as two
	// requests presenting the same token would.
	copies := make([]*session.RefreshToken, contenders)

	for i := range copies {
		found, err := store.RefreshTokens().FindByHash(ctx, realmID, token.Hash())
		if err != nil {
			t.Fatalf("reading: %v", err)
		}

		if err := found.Use(time.Now()); err != nil {
			t.Fatalf("using: %v", err)
		}

		copies[i] = found
	}

	var (
		start   = make(chan struct{})
		wins    = make(chan bool, contenders)
		errs    = make(chan error, contenders)
		waiting sync.WaitGroup
	)

	for _, mine := range copies {
		waiting.Go(func() {
			<-start

			err := store.InTx(ctx, func(tx service.Store) error {
				// A read first: under MySQL's REPEATABLE READ it pins a snapshot
				// in which the token is unused, and Consume must not decide on it.
				if _, err := tx.RefreshTokens().FindByHash(ctx, realmID, mine.Hash()); err != nil {
					return err
				}

				won, err := tx.RefreshTokens().Consume(ctx, mine)
				if err != nil {
					return err
				}

				wins <- won

				return nil
			})
			if err != nil {
				errs <- err
			}
		})
	}

	close(start)
	waiting.Wait()
	close(wins)
	close(errs)

	for err := range errs {
		t.Errorf("a contender failed: %v", err)
	}

	won, lost := 0, 0

	for win := range wins {
		if win {
			won++
		} else {
			lost++
		}
	}

	if won != 1 || lost != contenders-1 {
		t.Errorf("want exactly one winner among %d, got %d winners and %d losers", contenders, won, lost)
	}
}

func TestDeletingASessionRemovesItsRefreshTokens(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertASessionTakesItsRefreshTokensAlong(t, db, store)
}

// Shared with integration_test.go: the child's SET NULL and the session's
// CASCADE fire on the same rows in one statement.
func assertASessionTakesItsRefreshTokensAlong(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()
	realmID := seedRealm(t, store, "session-cascade")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	kept := seedSession(t, store, owner)
	removed := seedSession(t, store, owner)
	keptToken := seedRefreshToken(t, store, kept, uuid.Nil)
	parent := seedRefreshToken(t, store, removed, uuid.Nil)
	child := seedRefreshToken(t, store, removed, parent.ID())

	// Raw SQL: no repository method deletes a session.
	if err := db.Exec("DELETE FROM sessions WHERE id = ?", removed.ID().String()).Error; err != nil {
		t.Fatalf("deleting the session: %v", err)
	}

	for _, gone := range []*session.RefreshToken{parent, child} {
		_, err := store.RefreshTokens().FindByHash(ctx, realmID, gone.Hash())
		if !errors.Is(err, session.ErrTokenNotFound) {
			t.Errorf("want the token gone with its session, got %v", err)
		}
	}

	if _, err := store.RefreshTokens().FindByHash(ctx, realmID, keptToken.Hash()); err != nil {
		t.Errorf("the other session's token must survive: %v", err)
	}
}

func TestDeletingAParentTokenLeavesTheChildWithoutAParent(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertAChildOutlivesItsParentToken(t, db, store)
}

// Shared with integration_test.go.
func assertAChildOutlivesItsParentToken(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()
	realmID := seedRealm(t, store, "parent-cascade")
	owner := seedSession(t, store, seedIdentity(t, store, realmID, "ada@example.com", ""))

	parent := seedRefreshToken(t, store, owner, uuid.Nil)
	child := seedRefreshToken(t, store, owner, parent.ID())

	if err := db.Exec("DELETE FROM refresh_tokens WHERE id = ?", parent.ID().String()).Error; err != nil {
		t.Fatalf("deleting the parent: %v", err)
	}

	got, err := store.RefreshTokens().FindByHash(ctx, realmID, child.Hash())
	if err != nil {
		t.Fatalf("want the child to rehydrate without its parent, got %v", err)
	}

	if got.HasParent() || got.ID() != child.ID() {
		t.Errorf("want the child with no parent, got %s with parent %s", got.ID(), got.ParentID())
	}
}

func makeRefreshToken(t *testing.T, owner *session.Session, parentID uuid.UUID) *session.RefreshToken {
	t.Helper()

	_, hash, err := session.GenerateSecret()
	if err != nil {
		t.Fatalf("generating a secret: %v", err)
	}

	now := time.Now()

	built, err := session.NewRefreshToken(owner.RealmID(), owner.ID(), parentID, hash, now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("building a refresh token: %v", err)
	}

	return built
}

func seedRefreshToken(
	t *testing.T, store service.Store, owner *session.Session, parentID uuid.UUID,
) *session.RefreshToken {
	t.Helper()

	created := makeRefreshToken(t, owner, parentID)

	if err := store.RefreshTokens().Create(context.Background(), created); err != nil {
		t.Fatalf("creating a refresh token: %v", err)
	}

	return created
}
