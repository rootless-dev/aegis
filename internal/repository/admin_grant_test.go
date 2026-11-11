package repository_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/admin"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/repository"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

func TestAdminGrantRoundTripsBothKinds(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertAdminGrantRoundTrips(t, db, store)
}

// Shared with integration_test.go. The columns are read around the repository:
// the marker has no field on the aggregate to be read back through.
func assertAdminGrantRoundTrips(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()
	home := seedRealm(t, store, "grant-roundtrip")
	target := seedRealm(t, store, "grant-roundtrip-target")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	global := seedGrant(t, store, makeGlobalGrant(t, owner))
	targeted := seedGrant(t, store, makeRealmGrant(t, owner, target))

	got, err := store.AdminGrants().ListByIdentity(ctx, home, owner.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("want 2 grants, got %d", len(got))
	}

	for i, want := range []*admin.Grant{global, targeted} {
		if got[i].ID() != want.ID() || got[i].RealmID() != home || got[i].IdentityID() != owner.ID() {
			t.Errorf("grant %d: got %s in realm %s for identity %s",
				i, got[i].ID(), got[i].RealmID(), got[i].IdentityID())
		}

		if got[i].TargetRealmID() != want.TargetRealmID() || got[i].Global() != want.Global() {
			t.Errorf("grant %d: want target %s, got %s", i, want.TargetRealmID(), got[i].TargetRealmID())
		}

		if !got[i].CreatedAt().Equal(want.CreatedAt()) {
			t.Errorf("grant %d: created_at: want %s, got %s", i, want.CreatedAt(), got[i].CreatedAt())
		}
	}

	if !isNull(t, db, "admin_grants", "target_realm_id", global.ID()) {
		t.Error("a global grant must store a NULL target")
	}

	if countGrants(t, db, "id = ? AND global_marker = 'y'", global.ID().String()) != 1 {
		t.Error("a global grant must store the marker")
	}

	if !isNull(t, db, "admin_grants", "global_marker", targeted.ID()) {
		t.Error("a realm grant must store a NULL marker")
	}

	if countGrants(t, db, "id = ? AND target_realm_id = ?", targeted.ID().String(), target.String()) != 1 {
		t.Error("a realm grant must store its target")
	}
}

func TestAdminGrantRefusesADuplicateOfEitherKind(t *testing.T) {
	assertDuplicateGrantsAreRefused(t, newStore(t))
}

// Shared with integration_test.go.
func assertDuplicateGrantsAreRefused(t *testing.T, store service.Store) {
	t.Helper()

	ctx := context.Background()
	home := seedRealm(t, store, "grant-duplicate")
	target := seedRealm(t, store, "grant-duplicate-target")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	seedGrant(t, store, makeGlobalGrant(t, owner))
	seedGrant(t, store, makeRealmGrant(t, owner, target))

	if err := store.AdminGrants().Create(ctx, makeGlobalGrant(t, owner)); !errors.Is(err, admin.ErrExists) {
		t.Errorf("a second global grant: want admin.ErrExists, got %v", err)
	}

	err := store.AdminGrants().Create(ctx, makeRealmGrant(t, owner, target))
	if !errors.Is(err, admin.ErrExists) {
		t.Errorf("a second grant over the same realm: want admin.ErrExists, got %v", err)
	}

	got, err := store.AdminGrants().ListByIdentity(ctx, home, owner.ID())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(got) != 2 {
		t.Errorf("want the 2 original grants, got %d", len(got))
	}
}

// The primary key's column, admin_grants.id, is a prefix of
// admin_grants.identity_id, which both unique indexes name.
func TestAdminGrantADuplicateIDIsNotReportedAsAnExistingGrant(t *testing.T) {
	store := newStore(t)
	home := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, home, "ada@example.com", "")
	other := seedIdentity(t, store, home, "grace@example.com", "")

	first := seedGrant(t, store, makeGlobalGrant(t, owner))

	sameID, err := admin.Rehydrate(first.ID(), home, other.ID(), uuid.Nil, first.CreatedAt())
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	err = store.AdminGrants().Create(context.Background(), sameID)
	if err == nil || errors.Is(err, admin.ErrExists) {
		t.Errorf("want the driver error passed through, got %v", err)
	}
}

func TestAdminGrantAnIdentityHoldsAGlobalAndTwoRealmGrants(t *testing.T) {
	store := newStore(t)
	home := seedRealm(t, store, "master")
	acme := seedRealm(t, store, "acme")
	globex := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	want := []uuid.UUID{
		seedGrant(t, store, makeRealmGrant(t, owner, acme)).ID(),
		seedGrant(t, store, makeGlobalGrant(t, owner)).ID(),
		seedGrant(t, store, makeRealmGrant(t, owner, globex)).ID(),
	}

	got, err := store.AdminGrants().ListByIdentity(context.Background(), home, owner.ID())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, got, want)
}

func TestAdminGrantTwoIdentitiesHoldTheSameGrants(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "master")
	acme := seedRealm(t, store, "acme")
	ada := seedIdentity(t, store, home, "ada@example.com", "")
	grace := seedIdentity(t, store, home, "grace@example.com", "")

	for _, owner := range []*identity.Identity{ada, grace} {
		global := seedGrant(t, store, makeGlobalGrant(t, owner))
		targeted := seedGrant(t, store, makeRealmGrant(t, owner, acme))

		got, err := store.AdminGrants().ListByIdentity(ctx, home, owner.ID())
		if err != nil {
			t.Fatalf("listing: %v", err)
		}

		assertGrantIDs(t, got, []uuid.UUID{global.ID(), targeted.ID()})
	}
}

func TestAdminGrantListsAreScopedToTheHoldersRealm(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "master")
	other := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	seedGrant(t, store, makeGlobalGrant(t, owner))
	// The target is not the holder's realm: listing by it must find nothing.
	seedGrant(t, store, makeRealmGrant(t, owner, other))

	byIdentity, err := store.AdminGrants().ListByIdentity(ctx, other, owner.ID())
	if err != nil {
		t.Fatalf("listing by identity: %v", err)
	}

	if len(byIdentity) != 0 {
		t.Errorf("by identity: want nothing through another realm, got %d", len(byIdentity))
	}

	byRealm, err := store.AdminGrants().ListByRealm(ctx, other, 10)
	if err != nil {
		t.Fatalf("listing by realm: %v", err)
	}

	if len(byRealm) != 0 {
		t.Errorf("by realm: want nothing through another realm, got %d", len(byRealm))
	}
}

func TestAdminGrantListByIdentityLeavesOtherIdentitiesOut(t *testing.T) {
	store := newStore(t)
	home := seedRealm(t, store, "master")
	ada := seedIdentity(t, store, home, "ada@example.com", "")
	grace := seedIdentity(t, store, home, "grace@example.com", "")

	mine := seedGrant(t, store, makeGlobalGrant(t, ada))
	seedGrant(t, store, makeGlobalGrant(t, grace))

	got, err := store.AdminGrants().ListByIdentity(context.Background(), home, ada.ID())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, got, []uuid.UUID{mine.ID()})
}

func TestAdminGrantListByRealmIsOldestFirstAndHonoursTheLimit(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "master")
	elsewhere := seedRealm(t, store, "globex")
	ada := seedIdentity(t, store, home, "ada@example.com", "")
	grace := seedIdentity(t, store, home, "grace@example.com", "")
	stranger := seedIdentity(t, store, elsewhere, "linus@example.com", "")

	want := []uuid.UUID{
		seedGrant(t, store, makeGlobalGrant(t, grace)).ID(),
		seedGrant(t, store, makeRealmGrant(t, ada, elsewhere)).ID(),
		seedGrant(t, store, makeGlobalGrant(t, ada)).ID(),
	}

	seedGrant(t, store, makeGlobalGrant(t, stranger))

	all, err := store.AdminGrants().ListByRealm(ctx, home, 10)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, all, want)

	limited, err := store.AdminGrants().ListByRealm(ctx, home, 2)
	if err != nil {
		t.Fatalf("listing with a limit: %v", err)
	}

	assertGrantIDs(t, limited, want[:2])
}

func TestAdminGrantListByRealmRefusesANonPositiveLimit(t *testing.T) {
	store := newStore(t)
	home := seedRealm(t, store, "master")

	for _, limit := range []int{0, -1} {
		found, err := store.AdminGrants().ListByRealm(context.Background(), home, limit)
		if !errors.Is(err, service.ErrLimitNotPositive) {
			t.Errorf("limit %d: want ErrLimitNotPositive, got %v", limit, err)
		}

		if found != nil {
			t.Errorf("limit %d: no rows may come back with an error", limit)
		}
	}
}

func TestAdminGrantDeleteRemovesOnlyThatGrant(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "master")
	acme := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	removed := seedGrant(t, store, makeGlobalGrant(t, owner))
	kept := seedGrant(t, store, makeRealmGrant(t, owner, acme))

	if err := store.AdminGrants().Delete(ctx, home, removed.ID()); err != nil {
		t.Fatalf("deleting: %v", err)
	}

	got, err := store.AdminGrants().ListByIdentity(ctx, home, owner.ID())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, got, []uuid.UUID{kept.ID()})

	if err := store.AdminGrants().Delete(ctx, home, removed.ID()); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("deleting it again: want admin.ErrNotFound, got %v", err)
	}
}

func TestAdminGrantDeleteCannotCrossRealms(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "master")
	acme := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	created := seedGrant(t, store, makeRealmGrant(t, owner, acme))

	// Through the target realm too: realmID is the holder's, never the target's.
	if err := store.AdminGrants().Delete(ctx, acme, created.ID()); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("want admin.ErrNotFound, got %v", err)
	}

	got, err := store.AdminGrants().ListByIdentity(ctx, home, owner.ID())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, got, []uuid.UUID{created.ID()})
}

func TestDeletingAnIdentityRemovesItsGrantsAndNoOthers(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	home := seedRealm(t, store, "master")
	acme := seedRealm(t, store, "acme")
	removed := seedIdentity(t, store, home, "ada@example.com", "")
	kept := seedIdentity(t, store, home, "grace@example.com", "")

	seedGrant(t, store, makeGlobalGrant(t, removed))
	seedGrant(t, store, makeRealmGrant(t, removed, acme))

	want := []uuid.UUID{
		seedGrant(t, store, makeGlobalGrant(t, kept)).ID(),
		seedGrant(t, store, makeRealmGrant(t, kept, acme)).ID(),
	}

	if err := db.Exec("DELETE FROM identities WHERE id = ?", removed.ID().String()).Error; err != nil {
		t.Fatalf("deleting the identity: %v", err)
	}

	got, err := store.AdminGrants().ListByRealm(ctx, home, 10)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, got, want)
}

func TestDeletingTheHoldersRealmRemovesItsGrantsAndNoOthers(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	removed := seedRealm(t, store, "master")
	other := seedRealm(t, store, "globex")
	acme := seedRealm(t, store, "acme")
	gone := seedIdentity(t, store, removed, "ada@example.com", "")
	kept := seedIdentity(t, store, other, "grace@example.com", "")

	seedGrant(t, store, makeGlobalGrant(t, gone))
	seedGrant(t, store, makeRealmGrant(t, gone, acme))

	want := []uuid.UUID{
		seedGrant(t, store, makeGlobalGrant(t, kept)).ID(),
		seedGrant(t, store, makeRealmGrant(t, kept, acme)).ID(),
	}

	// Raw SQL: no repository method deletes a realm, on purpose.
	if err := db.Exec("DELETE FROM realms WHERE id = ?", removed.String()).Error; err != nil {
		t.Fatalf("deleting the realm: %v", err)
	}

	if left := countGrants(t, db, "realm_id = ?", removed.String()); left != 0 {
		t.Errorf("want the realm's grants gone with it, got %d", left)
	}

	got, err := store.AdminGrants().ListByRealm(ctx, other, 10)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, got, want)
}

func TestDeletingATargetRealmRemovesTheGrantsOverItAndNoOthers(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertATargetRealmTakesItsGrantsAlong(t, db, store)
}

// Shared with integration_test.go.
func assertATargetRealmTakesItsGrantsAlong(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()
	home := seedRealm(t, store, "grant-cascade")
	removed := seedRealm(t, store, "grant-cascade-removed")
	kept := seedRealm(t, store, "grant-cascade-kept")
	ada := seedIdentity(t, store, home, "ada@example.com", "")
	grace := seedIdentity(t, store, home, "grace@example.com", "")

	seedGrant(t, store, makeRealmGrant(t, ada, removed))
	seedGrant(t, store, makeRealmGrant(t, grace, removed))

	want := []uuid.UUID{
		seedGrant(t, store, makeGlobalGrant(t, ada)).ID(),
		seedGrant(t, store, makeRealmGrant(t, ada, kept)).ID(),
	}

	if err := db.Exec("DELETE FROM realms WHERE id = ?", removed.String()).Error; err != nil {
		t.Fatalf("deleting the target realm: %v", err)
	}

	got, err := store.AdminGrants().ListByRealm(ctx, home, 10)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	assertGrantIDs(t, got, want)

	if _, err := store.Identities().FindByID(ctx, home, grace.ID()); err != nil {
		t.Errorf("the holder must outlive the realm its grant applied to: %v", err)
	}
}

func TestHasGlobalAdministratorTruthTable(t *testing.T) {
	assertHasGlobalAdministratorTruthTable(t, newStore(t))
}

// Shared with integration_test.go. Every realm is seeded before any is asked,
// so each negative answer is given with a complete administrator one realm away.
func assertHasGlobalAdministratorTruthTable(t *testing.T, store service.Store) {
	t.Helper()

	ctx := context.Background()
	target := seedRealm(t, store, "admin-target")

	cases := []struct {
		slug                                 string
		global, targeted, password, disabled bool
		want                                 bool
	}{
		{slug: "admin-no-identity"},
		{slug: "admin-no-grant", password: true},
		{slug: "admin-realm-grant-only", targeted: true, password: true},
		{slug: "admin-no-credential", global: true},
		{slug: "admin-disabled", global: true, password: true, disabled: true},
		{slug: "admin-complete", global: true, password: true, want: true},
	}

	realms := make([]uuid.UUID, len(cases))

	for i, c := range cases {
		realms[i] = seedRealm(t, store, c.slug)

		if c.slug == "admin-no-identity" {
			continue
		}

		owner := seedIdentity(t, store, realms[i], "ada@example.com", "")

		if c.global {
			seedGrant(t, store, makeGlobalGrant(t, owner))
		}

		if c.targeted {
			seedGrant(t, store, makeRealmGrant(t, owner, target))
		}

		if c.password {
			// must_change set: a temporary password still signs in.
			if err := store.Credentials().Create(ctx, makeCredential(t, owner, true)); err != nil {
				t.Fatalf("%s: seeding the credential: %v", c.slug, err)
			}
		}

		if c.disabled {
			if err := owner.SetStatus(identity.StatusDisabled); err != nil {
				t.Fatalf("%s: disabling: %v", c.slug, err)
			}

			if err := store.Identities().Update(ctx, owner); err != nil {
				t.Fatalf("%s: storing the disabled identity: %v", c.slug, err)
			}
		}
	}

	// The grant and the password on two different identities of one realm.
	split := seedRealm(t, store, "admin-split")
	seedGrant(t, store, makeGlobalGrant(t, seedIdentity(t, store, split, "ada@example.com", "")))

	withPassword := seedIdentity(t, store, split, "grace@example.com", "")
	if err := store.Credentials().Create(ctx, makeCredential(t, withPassword, false)); err != nil {
		t.Fatalf("admin-split: seeding the credential: %v", err)
	}

	for i, c := range cases {
		got, err := store.AdminGrants().HasGlobalAdministrator(ctx, realms[i])
		if err != nil {
			t.Fatalf("%s: %v", c.slug, err)
		}

		if got != c.want {
			t.Errorf("%s: want %v, got %v", c.slug, c.want, got)
		}
	}

	got, err := store.AdminGrants().HasGlobalAdministrator(ctx, split)
	if err != nil {
		t.Fatalf("admin-split: %v", err)
	}

	if got {
		t.Error("admin-split: the grant and the password must belong to the same identity")
	}
}

func TestInTxRollsBackAnAdminGrant(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "master")
	owner := seedIdentity(t, store, home, "ada@example.com", "")
	sentinel := errors.New("stop")

	err := store.InTx(ctx, func(tx service.Store) error {
		if err := tx.AdminGrants().Create(ctx, makeGlobalGrant(t, owner)); err != nil {
			return err
		}

		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("the callback error must reach the caller, got %v", err)
	}

	got, err := store.AdminGrants().ListByIdentity(ctx, home, owner.ID())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("the grant survived a rollback: %d rows", len(got))
	}
}

func makeGlobalGrant(t *testing.T, owner *identity.Identity) *admin.Grant {
	t.Helper()

	built, err := admin.NewGlobal(owner.RealmID(), owner.ID())
	if err != nil {
		t.Fatalf("building a global grant: %v", err)
	}

	return built
}

func makeRealmGrant(t *testing.T, owner *identity.Identity, target uuid.UUID) *admin.Grant {
	t.Helper()

	built, err := admin.NewForRealm(owner.RealmID(), owner.ID(), target)
	if err != nil {
		t.Fatalf("building a realm grant: %v", err)
	}

	return built
}

func seedGrant(t *testing.T, store service.Store, g *admin.Grant) *admin.Grant {
	t.Helper()

	if err := store.AdminGrants().Create(context.Background(), g); err != nil {
		t.Fatalf("creating a grant: %v", err)
	}

	return g
}

func assertGrantIDs(t *testing.T, got []*admin.Grant, want []uuid.UUID) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("want %d grants, got %d", len(want), len(got))
	}

	for i := range want {
		if got[i].ID() != want[i] {
			t.Errorf("position %d: want %s, got %s", i, want[i], got[i].ID())
		}
	}
}

func countGrants(t *testing.T, db *gorm.DB, where string, args ...any) int64 {
	t.Helper()

	var count int64

	if err := db.Raw("SELECT COUNT(*) FROM admin_grants WHERE "+where, args...).Scan(&count).Error; err != nil {
		t.Fatalf("counting grants: %v", err)
	}

	return count
}

// Raw, to hand the driver's own text for a duplicate to the caller.
func insertRawGrant(db *gorm.DB, g *admin.Grant) error {
	var target, marker any
	if g.Global() {
		marker = "y"
	} else {
		target = g.TargetRealmID().String()
	}

	return db.Exec(
		"INSERT INTO admin_grants (id, realm_id, identity_id, target_realm_id, global_marker, created_at) "+
			"VALUES (?, ?, ?, ?, ?, ?)",
		g.ID().String(), g.RealmID().String(), g.IdentityID().String(), target, marker,
		repository.Timestamp(g.CreatedAt()),
	).Error
}

// The text translate keys off for each index, straight from SQLite.
func TestAdminGrantDuplicatesNameTheColumnsTranslateReads(t *testing.T) {
	db, store := newStoreAndDB(t)

	global, targeted := duplicateGrantTexts(t, db, store)

	want := "UNIQUE constraint failed: admin_grants.identity_id, admin_grants.global_marker"
	if !strings.HasSuffix(global, want) {
		t.Errorf("global: want it to end in %q, got %q", want, global)
	}

	want = "UNIQUE constraint failed: admin_grants.identity_id, admin_grants.target_realm_id"
	if !strings.HasSuffix(targeted, want) {
		t.Errorf("target: want it to end in %q, got %q", want, targeted)
	}
}

// Shared with integration_test.go.
func duplicateGrantTexts(t *testing.T, db *gorm.DB, store service.Store) (string, string) {
	t.Helper()

	home := seedRealm(t, store, "grant-text")
	target := seedRealm(t, store, "grant-text-target")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	seedGrant(t, store, makeGlobalGrant(t, owner))
	seedGrant(t, store, makeRealmGrant(t, owner, target))

	global := insertRawGrant(db, makeGlobalGrant(t, owner))
	if global == nil {
		t.Fatal("want uq_admin_grants_global to refuse it, got no error")
	}

	targeted := insertRawGrant(db, makeRealmGrant(t, owner, target))
	if targeted == nil {
		t.Fatal("want uq_admin_grants_target to refuse it, got no error")
	}

	t.Logf("driver text, global: %v", global)
	t.Logf("driver text, target: %v", targeted)

	return global.Error(), targeted.Error()
}
