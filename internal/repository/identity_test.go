package repository_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

func TestIdentityRoundTrip(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	// Rehydrated rather than built by New, to pin a non-zero microsecond and a
	// status other than the default.
	createdAt := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)
	updatedAt := time.Date(2026, 3, 4, 5, 6, 8, 654321000, time.UTC)

	want, err := identity.Rehydrate(
		id, realmID, "ada@example.com", "ada", identity.StatusDisabled, createdAt, updatedAt,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Identities().Create(ctx, want); err != nil {
		t.Fatalf("creating: %v", err)
	}

	lookups := map[string]func() (*identity.Identity, error){
		"by id": func() (*identity.Identity, error) {
			return store.Identities().FindByID(ctx, realmID, id)
		},
		"by email": func() (*identity.Identity, error) {
			return store.Identities().FindByEmail(ctx, realmID, "ada@example.com")
		},
		"by username": func() (*identity.Identity, error) {
			return store.Identities().FindByUsername(ctx, realmID, "ada")
		},
	}

	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			got, err := lookup()
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}

			if got.ID() != id || got.RealmID() != realmID {
				t.Errorf("want %s in %s, got %s in %s", id, realmID, got.ID(), got.RealmID())
			}

			if got.Email() != "ada@example.com" || got.Username() != "ada" {
				t.Errorf("got email %q and username %q", got.Email(), got.Username())
			}

			if got.Status() != identity.StatusDisabled {
				t.Errorf("status: got %q", got.Status())
			}

			if !got.CreatedAt().Equal(createdAt) || !got.UpdatedAt().Equal(updatedAt) {
				t.Errorf("timestamps: got %s and %s", got.CreatedAt(), got.UpdatedAt())
			}
		})
	}
}

func TestIdentityWithoutUsernameIsStoredAsNull(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	first := seedIdentity(t, store, realmID, "ada@example.com", "")

	// Two empty strings would collide in uq_identities_username; two NULLs do not.
	if err := store.Identities().Create(ctx, makeIdentity(t, realmID, "grace@example.com", "")); err != nil {
		t.Fatalf("a second identity without a username must be allowed, got %v", err)
	}

	if stored := rawRow(t, db, "identities", first.ID())["username"]; stored != nil {
		t.Errorf("want NULL in the column, got %#v", stored)
	}

	got, err := store.Identities().FindByID(ctx, realmID, first.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.HasUsername() {
		t.Errorf("want no username, got %q", got.Username())
	}

	if _, err := store.Identities().FindByUsername(ctx, realmID, ""); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("an empty username must match no row, got %v", err)
	}
}

func TestIdentityCreateTranslatesTheUniqueViolations(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	seedIdentity(t, store, realmID, "ada@example.com", "ada")

	// Each duplicate collides on one index only: which violation an engine
	// reports first is not something to depend on.
	sameEmail := makeIdentity(t, realmID, "ada@example.com", "other")
	if err := store.Identities().Create(ctx, sameEmail); !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("want identity.ErrEmailTaken, got %v", err)
	}

	sameUsername := makeIdentity(t, realmID, "other@example.com", "ada")
	if err := store.Identities().Create(ctx, sameUsername); !errors.Is(err, identity.ErrUsernameTaken) {
		t.Errorf("want identity.ErrUsernameTaken, got %v", err)
	}
}

// uniqueness is per realm: the indexes lead with realm_id.
func TestIdentityAllowsTheSameEmailAndUsernameInAnotherRealm(t *testing.T) {
	store := newStore(t)
	first := seedRealm(t, store, "acme")
	second := seedRealm(t, store, "globex")

	seedIdentity(t, store, first, "ada@example.com", "ada")

	err := store.Identities().Create(context.Background(), makeIdentity(t, second, "ada@example.com", "ada"))
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}

func TestIdentityLookupsAreScopedToTheRealm(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")

	created := seedIdentity(t, store, home, "ada@example.com", "ada")

	if _, err := store.Identities().FindByID(ctx, other, created.ID()); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("by id: want identity.ErrNotFound, got %v", err)
	}

	if _, err := store.Identities().FindByEmail(ctx, other, "ada@example.com"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("by email: want identity.ErrNotFound, got %v", err)
	}

	if _, err := store.Identities().FindByUsername(ctx, other, "ada"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("by username: want identity.ErrNotFound, got %v", err)
	}

	listed, err := store.Identities().List(ctx, other, service.IdentityQuery{Limit: 10})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(listed) != 0 {
		t.Errorf("want nothing listed from another realm, got %d", len(listed))
	}
}

func TestIdentityFindReportsNotFoundInTheDomainVocabulary(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	if _, err := store.Identities().FindByID(ctx, realmID, uuid.New()); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("by id: want identity.ErrNotFound, got %v", err)
	}

	if _, err := store.Identities().FindByEmail(ctx, realmID, "nobody@example.com"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("by email: want identity.ErrNotFound, got %v", err)
	}

	if _, err := store.Identities().FindByUsername(ctx, realmID, "nobody"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("by username: want identity.ErrNotFound, got %v", err)
	}
}

// The aggregate has no setter for these columns, so the diverging copy comes
// through Rehydrate: what a future setter would hand to Update.
func TestIdentityUpdateWritesOnlyStatusAndUpdatedAt(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	created := seedIdentity(t, store, realmID, "ada@example.com", "ada")
	before := rawRow(t, db, "identities", created.ID())

	updatedAt := created.UpdatedAt().Add(time.Hour)

	diverging, err := identity.Rehydrate(
		created.ID(), realmID, "moved@example.com", "moved", identity.StatusDisabled,
		created.CreatedAt().Add(-time.Hour), updatedAt,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Identities().Update(ctx, diverging); err != nil {
		t.Fatalf("updating: %v", err)
	}

	after := rawRow(t, db, "identities", created.ID())

	for _, column := range []string{"id", "realm_id", "email", "username", "created_at"} {
		if fmt.Sprint(before[column]) != fmt.Sprint(after[column]) {
			t.Errorf("%s must be untouched by Update: %v became %v", column, before[column], after[column])
		}
	}

	got, err := store.Identities().FindByID(ctx, realmID, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Status() != identity.StatusDisabled || !got.UpdatedAt().Equal(updatedAt) {
		t.Errorf("want disabled at %s, got %q at %s", updatedAt, got.Status(), got.UpdatedAt())
	}
}

func TestIdentityUpdateCannotCrossRealms(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")

	created := seedIdentity(t, store, home, "ada@example.com", "ada")

	foreign, err := identity.Rehydrate(
		created.ID(), other, created.Email(), created.Username(), identity.StatusDisabled,
		created.CreatedAt(), created.UpdatedAt().Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Identities().Update(ctx, foreign); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("want identity.ErrNotFound, got %v", err)
	}

	got, err := store.Identities().FindByID(ctx, home, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Status() != identity.StatusActive {
		t.Errorf("the row in its own realm must be untouched, got %q", got.Status())
	}
}

func TestIdentityUpdateOfAMissingRowReportsNotFound(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	never := makeIdentity(t, realmID, "ada@example.com", "")

	if err := store.Identities().Update(context.Background(), never); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("want identity.ErrNotFound, got %v", err)
	}
}

func TestIdentityListPagesByKeyset(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	// Explicit, ascending ids: three v7s minted within one millisecond are not
	// guaranteed to sort in creation order.
	var ids []uuid.UUID

	for n, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		id := uuid.MustParse(fmt.Sprintf("01900000-0000-7000-8000-00000000000%d", n+1))
		now := time.Now().UTC()

		built, err := identity.Rehydrate(id, realmID, email, "", identity.StatusActive, now, now)
		if err != nil {
			t.Fatalf("building: %v", err)
		}

		if err := store.Identities().Create(ctx, built); err != nil {
			t.Fatalf("creating %q: %v", email, err)
		}

		ids = append(ids, id)
	}

	first, err := store.Identities().List(ctx, realmID, service.IdentityQuery{Limit: 2})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(first) != 2 || first[0].ID() != ids[0] || first[1].ID() != ids[1] {
		t.Fatalf("want the first two by id ascending, got %d rows", len(first))
	}

	second, err := store.Identities().List(ctx, realmID, service.IdentityQuery{After: first[1].ID(), Limit: 2})
	if err != nil {
		t.Fatalf("listing the second page: %v", err)
	}

	if len(second) != 1 || second[0].ID() != ids[2] {
		t.Fatalf("want the remaining one, got %d rows", len(second))
	}
}

func TestIdentityListFiltersByStatus(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	seedIdentity(t, store, realmID, "kept@example.com", "")
	disabled := seedIdentity(t, store, realmID, "gone@example.com", "")

	if err := disabled.SetStatus(identity.StatusDisabled); err != nil {
		t.Fatalf("disabling: %v", err)
	}

	if err := store.Identities().Update(ctx, disabled); err != nil {
		t.Fatalf("updating: %v", err)
	}

	active, err := store.Identities().List(ctx, realmID, service.IdentityQuery{
		Limit: 10, Status: []identity.Status{identity.StatusActive},
	})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(active) != 1 || active[0].Email() != "kept@example.com" {
		t.Errorf("want only the active identity, got %d rows", len(active))
	}

	every, err := store.Identities().List(ctx, realmID, service.IdentityQuery{Limit: 10})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(every) != 2 {
		t.Errorf("an empty filter means every status: want 2, got %d", len(every))
	}
}

func TestIdentityListRefusesANonPositiveLimit(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	seedIdentity(t, store, realmID, "ada@example.com", "")

	for _, limit := range []int{0, -1} {
		found, err := store.Identities().List(context.Background(), realmID, service.IdentityQuery{Limit: limit})
		if !errors.Is(err, service.ErrLimitNotPositive) {
			t.Errorf("limit %d: want ErrLimitNotPositive, got %v", limit, err)
		}

		if found != nil {
			t.Errorf("limit %d: no rows may come back with an error", limit)
		}
	}
}

func makeIdentity(t *testing.T, realmID uuid.UUID, email, username string) *identity.Identity {
	t.Helper()

	built, err := identity.New(realmID, email, username)
	if err != nil {
		t.Fatalf("building an identity: %v", err)
	}

	return built
}

func seedIdentity(
	t *testing.T, store service.Store, realmID uuid.UUID, email, username string,
) *identity.Identity {
	t.Helper()

	created := makeIdentity(t, realmID, email, username)

	if err := store.Identities().Create(context.Background(), created); err != nil {
		t.Fatalf("creating an identity: %v", err)
	}

	return created
}

// rawRow reads one row around the repository, as the driver returns it.
func rawRow(t *testing.T, db *gorm.DB, table string, id uuid.UUID) map[string]any {
	t.Helper()

	row := map[string]any{}

	if err := db.Table(table).Where("id = ?", id.String()).Take(&row).Error; err != nil {
		t.Fatalf("reading %s raw: %v", table, err)
	}

	return row
}
