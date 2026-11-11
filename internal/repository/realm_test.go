package repository_test

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ncruces/go-sqlite3/gormlite"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/migrations"
	"github.com/rootless-dev/aegis/internal/repository"
	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

func newStore(t *testing.T) service.Store {
	t.Helper()

	_, store := newStoreAndDB(t)

	return store
}

// newStoreAndDB also hands out the *gorm.DB, for the tests that read or delete
// a row around the repository.
func newStoreAndDB(t *testing.T) (*gorm.DB, service.Store) {
	t.Helper()

	// The transaction settings and the foreign_keys pragma mirror
	// database.Open: a store built on looser terms than the running system
	// would prove nothing about it. SQLite enforces no foreign key without it.
	db, err := gorm.Open(
		gormlite.Open("file:"+filepath.Join(t.TempDir(), "aegis.db")+"?_pragma=foreign_keys(1)"),
		&gorm.Config{SkipDefaultTransaction: true, DisableNestedTransaction: true},
	)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	// The same files the boot applies, and every one of them in order: a copy
	// kept in a test proves nothing, and hardcoding 0001 would silently stop
	// applying migrations the day a 0002 lands.
	tree, err := migrations.For("sqlite")
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}

	entries, err := fs.ReadDir(tree, ".")
	if err != nil {
		t.Fatalf("reading the migration directory: %v", err)
	}

	// fs.ReadDir returns entries sorted by name, and the sequential prefixes
	// sort into apply order, so no sort of our own is needed here. The down
	// files are skipped rather than ordered around: they sort before their up
	// counterpart, so the first one added would drop the schema this is
	// building.
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}

		body, err := fs.ReadFile(tree, entry.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}

		if err := db.Exec(string(body)).Error; err != nil {
			t.Fatalf("applying %s: %v", entry.Name(), err)
		}
	}

	return db, repository.NewStore(db)
}

func makeRealm(t *testing.T, slug string) *realm.Realm {
	t.Helper()

	base, err := url.Parse("https://idp.example.com")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	created, err := realm.New(slug, "Display "+slug, base)
	if err != nil {
		t.Fatalf("creating %q: %v", slug, err)
	}

	return created
}

func TestRealmRoundTrip(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	want := makeRealm(t, "acme")

	if err := store.Realms().Create(ctx, want); err != nil {
		t.Fatalf("creating: %v", err)
	}

	got, err := store.Realms().FindBySlug(ctx, "acme")
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.ID() != want.ID() {
		t.Errorf("id: want %s, got %s", want.ID(), got.ID())
	}

	if got.Issuer() != want.Issuer() {
		t.Errorf("issuer: want %q, got %q", want.Issuer(), got.Issuer())
	}

	if got.Status() != realm.StatusActive {
		t.Errorf("status: got %q", got.Status())
	}

	// Microsecond, because that is the precision every dialect's column
	// carries and the only one a round trip can promise.
	if !got.CreatedAt().Equal(want.CreatedAt()) {
		t.Errorf("created_at: want %s, got %s", want.CreatedAt(), got.CreatedAt())
	}
}

func TestFindBySlugReportsNotFoundInTheDomainVocabulary(t *testing.T) {
	store := newStore(t)

	_, err := store.Realms().FindBySlug(context.Background(), "missing")
	if !errors.Is(err, realm.ErrNotFound) {
		t.Errorf("want realm.ErrNotFound, got %v", err)
	}
}

func TestCreateTranslatesTheUniqueViolations(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	first := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, first); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// A different base isolates the collision to the slug: the same base would
	// collide on issuer too, and SQLite reports only one violation per row.
	otherBase, err := url.Parse("https://other.example.com")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	duplicateSlug, err := realm.New("acme", "Display acme", otherBase)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := store.Realms().Create(ctx, duplicateSlug); !errors.Is(err, realm.ErrSlugTaken) {
		t.Errorf("want realm.ErrSlugTaken, got %v", err)
	}
}

// The mirror of the case above: same base, different slug, still collides on
// issuer alone is not reachable through DeriveIssuer (issuer always carries
// the slug), so the issuer collision is exercised directly against the
// store instead, through Reissue onto an issuer another realm already holds.
func TestReissueTranslatesTheIssuerViolation(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	first := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, first); err != nil {
		t.Fatalf("creating: %v", err)
	}

	second := makeRealm(t, "beta")
	if err := store.Realms().Create(ctx, second); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := store.Realms().Reissue(ctx, second.ID(), first.Issuer()); !errors.Is(err, realm.ErrIssuerTaken) {
		t.Errorf("want realm.ErrIssuerTaken, got %v", err)
	}
}

// FindBySlug has to see archived realms, or creation hands out a burned slug
// and the failure the archive exists to prevent comes back through the side
// door.
func TestFindBySlugSeesArchivedRealms(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	created := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := created.SetStatus(realm.StatusArchived); err != nil {
		t.Fatalf("archiving: %v", err)
	}

	if err := store.Realms().Update(ctx, created); err != nil {
		t.Fatalf("updating: %v", err)
	}

	got, err := store.Realms().FindBySlug(ctx, "acme")
	if err != nil {
		t.Fatalf("an archived realm must still be found: %v", err)
	}

	if got.Status() != realm.StatusArchived {
		t.Errorf("status: got %q", got.Status())
	}
}

func TestListPagesByKeyset(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for _, slug := range []string{"a-one", "b-two", "c-three"} {
		if err := store.Realms().Create(ctx, makeRealm(t, slug)); err != nil {
			t.Fatalf("creating %q: %v", slug, err)
		}
	}

	statuses := []realm.Status{realm.StatusActive, realm.StatusDisabled}

	first, err := store.Realms().List(ctx, service.RealmQuery{Limit: 2, Status: statuses})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(first) != 2 {
		t.Fatalf("want 2, got %d", len(first))
	}

	// Ordered by id, which is creation order because the identifier is a v7.
	if first[0].ID().String() >= first[1].ID().String() {
		t.Error("the page must be ordered by id ascending")
	}

	second, err := store.Realms().List(ctx, service.RealmQuery{
		After: first[1].ID(), Limit: 2, Status: statuses,
	})
	if err != nil {
		t.Fatalf("listing the second page: %v", err)
	}

	if len(second) != 1 {
		t.Fatalf("want the remaining 1, got %d", len(second))
	}

	if second[0].ID() == first[0].ID() || second[0].ID() == first[1].ID() {
		t.Error("the second page must not repeat the first")
	}
}

// The service clamps the page size before it reaches here, so this is the
// boundary refusing to depend on that: a zero limit reaching GORM emits
// LIMIT 0, which returns an empty page and no error to explain it.
// The service is the one place that decides what an unset limit becomes. If the
// repository invented a second answer, a caller reaching past the service — the
// admin API will — would page by that one instead, and a test holding two rows
// could not tell the two answers apart.
func TestListRefusesANonPositiveLimit(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for _, slug := range []string{"a-one", "b-two"} {
		if err := store.Realms().Create(ctx, makeRealm(t, slug)); err != nil {
			t.Fatalf("creating %q: %v", slug, err)
		}
	}

	for _, limit := range []int{0, -1} {
		found, err := store.Realms().List(ctx, service.RealmQuery{
			Limit:  limit,
			Status: []realm.Status{realm.StatusActive},
		})
		if !errors.Is(err, service.ErrLimitNotPositive) {
			t.Errorf("limit %d: want ErrLimitNotPositive, got %v", limit, err)
		}

		if found != nil {
			t.Errorf("limit %d: no rows may come back with an error", limit)
		}
	}
}

func TestListFiltersByStatus(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	kept := makeRealm(t, "kept")
	if err := store.Realms().Create(ctx, kept); err != nil {
		t.Fatalf("creating: %v", err)
	}

	gone := makeRealm(t, "gone")
	if err := store.Realms().Create(ctx, gone); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := gone.SetStatus(realm.StatusArchived); err != nil {
		t.Fatalf("archiving: %v", err)
	}

	if err := store.Realms().Update(ctx, gone); err != nil {
		t.Fatalf("updating: %v", err)
	}

	active, err := store.Realms().List(ctx, service.RealmQuery{
		Limit: 10, Status: []realm.Status{realm.StatusActive},
	})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(active) != 1 || active[0].Slug() != "kept" {
		t.Errorf("want only the active realm, got %d rows", len(active))
	}
}

// Update must not be able to move a column the aggregate calls immutable, even
// if a future setter lets one through.
func TestUpdateLeavesTheImmutableColumnsAlone(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	created := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := created.Rename("Renamed"); err != nil {
		t.Fatalf("renaming: %v", err)
	}

	if err := store.Realms().Update(ctx, created); err != nil {
		t.Fatalf("updating: %v", err)
	}

	got, err := store.Realms().FindByID(ctx, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.DisplayName() != "Renamed" {
		t.Errorf("display name: got %q", got.DisplayName())
	}

	if got.Slug() != "acme" || got.Issuer() != created.Issuer() {
		t.Error("slug and issuer must be untouched by Update")
	}

	if !got.CreatedAt().Equal(created.CreatedAt()) {
		t.Error("created_at must be untouched by Update")
	}
}

// rawRealm is the row as the engine holds it, read around the repository.
type rawRealm struct {
	Issuer         string `gorm:"column:issuer"`
	AccessTokenTTL int64  `gorm:"column:access_token_ttl"`
	SessionIdleTTL int64  `gorm:"column:session_idle_ttl"`
	SessionMaxTTL  int64  `gorm:"column:session_max_ttl"`
	PasswordGrant  bool   `gorm:"column:password_grant"`
}

func readRawRealm(t *testing.T, db *gorm.DB, id uuid.UUID) rawRealm {
	t.Helper()

	var row rawRealm

	err := db.Raw(
		"SELECT issuer, access_token_ttl, session_idle_ttl, session_max_ttl, password_grant FROM realms WHERE id = ?",
		id.String(),
	).Scan(&row).Error
	if err != nil {
		t.Fatalf("reading the raw row: %v", err)
	}

	return row
}

// The assert* helpers below also run against the selected engine, from
// integration_test.go.

func assertSettingsRoundTrip(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()

	plain := makeRealm(t, "plain")
	if err := store.Realms().Create(ctx, plain); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// Rehydrate is the only way to an aggregate whose TTLs are not the defaults.
	want := realm.Settings{
		AccessTokenTTL: 90 * time.Second,
		SessionIdleTTL: 10 * time.Minute,
		SessionMaxTTL:  time.Hour,
		PasswordGrant:  true,
	}

	seed := makeRealm(t, "tuned")

	tuned, err := realm.Rehydrate(
		seed.ID(), seed.Slug(), seed.DisplayName(), seed.Issuer(),
		seed.Status(), want, seed.CreatedAt(), seed.UpdatedAt(),
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if err := store.Realms().Create(ctx, tuned); err != nil {
		t.Fatalf("creating: %v", err)
	}

	gotPlain, err := store.Realms().FindByID(ctx, plain.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if gotPlain.Settings() != realm.DefaultSettings() {
		t.Errorf("defaults: got %+v", gotPlain.Settings())
	}

	gotTuned, err := store.Realms().FindByID(ctx, tuned.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if gotTuned.Settings() != want {
		t.Errorf("want %+v, got %+v", want, gotTuned.Settings())
	}

	wantRow := rawRealm{
		Issuer: tuned.Issuer(), AccessTokenTTL: 90, SessionIdleTTL: 600, SessionMaxTTL: 3600, PasswordGrant: true,
	}

	if row := readRawRealm(t, db, tuned.ID()); row != wantRow {
		t.Errorf("the columns hold whole seconds: want %+v, got %+v", wantRow, row)
	}
}

func assertUpdateWritesThePasswordGrantAndNothingElseNew(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	ctx := context.Background()

	created := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// Moved underneath the aggregate, which still holds the defaults and the
	// original issuer: an Update that wrote either would put them back.
	moved := "https://login.acme.com/realms/acme"

	err := db.Exec(
		"UPDATE realms SET issuer = ?, access_token_ttl = 60, session_idle_ttl = 120, session_max_ttl = 240 WHERE id = ?",
		moved, created.ID().String(),
	).Error
	if err != nil {
		t.Fatalf("rewriting the row: %v", err)
	}

	for _, enabled := range []bool{true, false, true} {
		created.SetPasswordGrant(enabled)

		if err := store.Realms().Update(ctx, created); err != nil {
			t.Fatalf("updating to %v: %v", enabled, err)
		}

		want := rawRealm{
			Issuer: moved, AccessTokenTTL: 60, SessionIdleTTL: 120, SessionMaxTTL: 240, PasswordGrant: enabled,
		}

		if row := readRawRealm(t, db, created.ID()); row != want {
			t.Errorf("want %+v, got %+v", want, row)
		}

		got, err := store.Realms().FindByID(ctx, created.ID())
		if err != nil {
			t.Fatalf("reading back: %v", err)
		}

		if got.Settings().PasswordGrant != enabled {
			t.Errorf("password grant: want %v, got %v", enabled, got.Settings().PasswordGrant)
		}
	}
}

// A row written by something that predates the four columns: the defaults are
// the schema's, and they have to agree with the domain's.
func assertARowWithoutTheSettingsReadsBackWithTheDefaults(t *testing.T, db *gorm.DB, store service.Store) {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Microsecond)

	err := db.Exec(
		"INSERT INTO realms (id, slug, display_name, issuer, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		uuid.Must(uuid.NewV7()).String(), "legacy", "Legacy", "https://idp.example.com/realms/legacy", "active", now, now,
	).Error
	if err != nil {
		t.Fatalf("inserting: %v", err)
	}

	got, err := store.Realms().FindBySlug(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Settings() != realm.DefaultSettings() {
		t.Errorf("want %+v, got %+v", realm.DefaultSettings(), got.Settings())
	}
}

func TestRealmSettingsRoundTrip(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertSettingsRoundTrip(t, db, store)
}

func TestUpdateWritesThePasswordGrantAndNotTheTTLsOrTheIssuer(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertUpdateWritesThePasswordGrantAndNothingElseNew(t, db, store)
}

func TestARealmRowWithoutTheSettingsReadsBackWithTheDefaults(t *testing.T) {
	db, store := newStoreAndDB(t)

	assertARowWithoutTheSettingsReadsBackWithTheDefaults(t, db, store)
}

// A value a DBA wrote that the domain would not: read, not silently clamped.
func TestARealmWithStoredSettingsOutOfRangeIsRefused(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()

	created := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// The second is large enough to overflow a time.Duration once in nanoseconds.
	for _, seconds := range []int64{36001, 1 << 62} {
		err := db.Exec("UPDATE realms SET session_idle_ttl = ? WHERE id = ?", seconds, created.ID().String()).Error
		if err != nil {
			// The schema may refuse it first, which is the same guarantee.
			continue
		}

		if _, err := store.Realms().FindByID(ctx, created.ID()); !errors.Is(err, realm.ErrSettingsInvalid) {
			t.Errorf("%d seconds: want ErrSettingsInvalid, got %v", seconds, err)
		}
	}
}

func TestReissueRewritesOnlyTheIssuer(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	created := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	moved := "https://login.acme.com/realms/acme"

	if err := store.Realms().Reissue(ctx, created.ID(), moved); err != nil {
		t.Fatalf("reissuing: %v", err)
	}

	got, err := store.Realms().FindByID(ctx, created.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Issuer() != moved {
		t.Errorf("issuer: want %q, got %q", moved, got.Issuer())
	}

	if got.Slug() != "acme" {
		t.Error("Reissue must touch nothing but the issuer")
	}
}

// Nesting opens no savepoint: the outermost InTx is the only rollback boundary
// there is. A caller that swallows an inner failure therefore commits what the
// inner step wrote, rather than committing a unit an invisible RollbackTo had
// already cut a hole in — which is what GORM does without
// DisableNestedTransaction.
func TestNestedInTxJoinsTheOuterTransaction(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sentinel := errors.New("stop")

	err := store.InTx(ctx, func(outer service.Store) error {
		if err := outer.Realms().Create(ctx, makeRealm(t, "outer")); err != nil {
			return err
		}

		inner := outer.InTx(ctx, func(tx service.Store) error {
			if err := tx.Realms().Create(ctx, makeRealm(t, "inner")); err != nil {
				return err
			}

			return sentinel
		})

		if !errors.Is(inner, sentinel) {
			t.Errorf("the inner error must reach its caller, got %v", inner)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("committing: %v", err)
	}

	for _, slug := range []string{"outer", "inner"} {
		if _, err := store.Realms().FindBySlug(ctx, slug); err != nil {
			t.Errorf("%q must be part of the one unit the outer InTx committed: %v", slug, err)
		}
	}
}

// Atomic creation is the entire argument for a pending status being
// unrepresentable, so it has to be a property the code holds and not a story.
func TestInTxRollsBackEveryWrite(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	sentinel := errors.New("stop")

	err := store.InTx(ctx, func(tx service.Store) error {
		if err := tx.Realms().Create(ctx, makeRealm(t, "first")); err != nil {
			return err
		}

		if err := tx.Realms().Create(ctx, makeRealm(t, "second")); err != nil {
			return err
		}

		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("the callback error must reach the caller, got %v", err)
	}

	for _, slug := range []string{"first", "second"} {
		if _, err := store.Realms().FindBySlug(ctx, slug); !errors.Is(err, realm.ErrNotFound) {
			t.Errorf("%q survived a rollback", slug)
		}
	}
}

func TestInTxCommitsWhenTheCallbackSucceeds(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	err := store.InTx(ctx, func(tx service.Store) error {
		return tx.Realms().Create(ctx, makeRealm(t, "kept"))
	})
	if err != nil {
		t.Fatalf("committing: %v", err)
	}

	if _, err := store.Realms().FindBySlug(ctx, "kept"); err != nil {
		t.Errorf("the write must survive a commit: %v", err)
	}
}

func TestFindByIDRejectsTheNilIdentifier(t *testing.T) {
	store := newStore(t)

	if _, err := store.Realms().FindByID(context.Background(), uuid.Nil); err == nil {
		t.Error("the nil identifier must not be looked up")
	}
}
