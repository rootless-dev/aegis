package service_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/service"
)

type fakeRepo struct {
	bySlug   map[string]*realm.Realm
	byID     map[uuid.UUID]*realm.Realm
	reissued map[uuid.UUID]string

	// lastQuery is what the clamping and filtering tests assert on: List
	// returns nothing, because what matters is the query the service built.
	lastQuery service.RealmQuery
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		bySlug:   map[string]*realm.Realm{},
		byID:     map[uuid.UUID]*realm.Realm{},
		reissued: map[uuid.UUID]string{},
	}
}

func (f *fakeRepo) Create(_ context.Context, r *realm.Realm) error {
	if _, taken := f.bySlug[r.Slug()]; taken {
		return realm.ErrSlugTaken
	}

	f.bySlug[r.Slug()] = r
	f.byID[r.ID()] = r

	return nil
}

func (f *fakeRepo) FindByID(_ context.Context, id uuid.UUID) (*realm.Realm, error) {
	if r, ok := f.byID[id]; ok {
		return r, nil
	}

	return nil, realm.ErrNotFound
}

func (f *fakeRepo) FindBySlug(_ context.Context, slug string) (*realm.Realm, error) {
	if r, ok := f.bySlug[slug]; ok {
		return r, nil
	}

	return nil, realm.ErrNotFound
}

func (f *fakeRepo) List(_ context.Context, q service.RealmQuery) ([]*realm.Realm, error) {
	f.lastQuery = q

	return nil, nil
}

func (f *fakeRepo) Update(_ context.Context, r *realm.Realm) error {
	f.bySlug[r.Slug()] = r
	f.byID[r.ID()] = r

	return nil
}

func (f *fakeRepo) Reissue(_ context.Context, id uuid.UUID, issuer string) error {
	stored, ok := f.byID[id]
	if !ok {
		return realm.ErrNotFound
	}

	// The aggregate has no issuer setter on purpose, so the fake rebuilds it
	// the way the real repository does: through the rehydration door.
	rebuilt, err := realm.Rehydrate(
		stored.ID(), stored.Slug(), stored.DisplayName(), issuer,
		stored.Status(), stored.CreatedAt(), stored.UpdatedAt(),
	)
	if err != nil {
		return err
	}

	f.byID[id] = rebuilt
	f.bySlug[rebuilt.Slug()] = rebuilt
	f.reissued[id] = issuer

	return nil
}

type fakeStore struct{ repo service.RealmRepository }

func (s fakeStore) Realms() service.RealmRepository { return s.repo }

func (s fakeStore) InTx(ctx context.Context, fn func(service.Store) error) error { return fn(s) }

func newService(t *testing.T) (*service.RealmService, *fakeRepo) {
	t.Helper()

	base, err := url.Parse("https://idp.example.com")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	repo := newFakeRepo()

	return service.NewRealmService(fakeStore{repo: repo}, base), repo
}

func TestCreateRefusesEveryReservedSlug(t *testing.T) {
	svc, _ := newService(t)

	// The padded forms belong here rather than in a test of their own: the
	// reserved list is compared by exact equality, so a slug that only becomes
	// "admin" after the aggregate trims it would pass the rule and then be
	// stored under the name the rule exists to protect.
	for _, slug := range []string{
		"master", "admin", "api", "aegis",
		" master ", "admin\t", "\napi", " aegis",
	} {
		if _, err := svc.Create(t.Context(), slug, "Whatever"); !errors.Is(err, realm.ErrSlugReserved) {
			t.Errorf("slug %q must be refused with ErrSlugReserved, got %v", slug, err)
		}
	}
}

func TestCreateAcceptsAnOrdinarySlug(t *testing.T) {
	svc, repo := newService(t)

	r, err := svc.Create(t.Context(), "acme", "Acme Inc")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if want := "https://idp.example.com/realms/acme"; r.Issuer() != want {
		t.Errorf("issuer: want %q, got %q", want, r.Issuer())
	}

	if _, stored := repo.bySlug["acme"]; !stored {
		t.Error("the realm must reach the repository")
	}
}

// The seed creates master, which Create refuses. If both went through the same
// door, the system could not start.
func TestEnsureMasterCreatesTheReservedRealm(t *testing.T) {
	svc, repo := newService(t)

	r, err := svc.EnsureMaster(t.Context(), false)
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if r.Slug() != "master" {
		t.Errorf("slug: got %q", r.Slug())
	}

	if want := "https://idp.example.com/realms/master"; r.Issuer() != want {
		t.Errorf("issuer: want %q, got %q", want, r.Issuer())
	}

	if _, stored := repo.bySlug["master"]; !stored {
		t.Error("the master realm must reach the repository")
	}
}

func TestEnsureMasterIsIdempotent(t *testing.T) {
	svc, _ := newService(t)

	first, err := svc.EnsureMaster(t.Context(), false)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	second, err := svc.EnsureMaster(t.Context(), false)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if first.ID() != second.ID() {
		t.Error("a second boot must not create a second master realm")
	}
}

// racingRepo is the losing replica: its lookup finds nothing and its insert
// finds the row there — the interleaving that turns a check-then-act seed into
// a crash loop.
type racingRepo struct {
	*fakeRepo

	hideFirstLookup bool

	// collision is which of the two unique constraints the engine names. Both
	// fire at once on a seed race — same slug, same issuer — and which one comes
	// back is engine business, so the recovery has to survive either.
	collision error
}

func (r *racingRepo) FindBySlug(ctx context.Context, slug string) (*realm.Realm, error) {
	if r.hideFirstLookup {
		r.hideFirstLookup = false

		return nil, realm.ErrNotFound
	}

	return r.fakeRepo.FindBySlug(ctx, slug)
}

func (r *racingRepo) Create(ctx context.Context, aggregate *realm.Realm) error {
	if err := r.fakeRepo.Create(ctx, aggregate); err != nil {
		return r.collision
	}

	return nil
}

// seedCollisions is what a losing replica may be told by the engine.
var seedCollisions = map[string]error{
	"slug":   realm.ErrSlugTaken,
	"issuer": realm.ErrIssuerTaken,
}

// racing builds a service whose repository already holds a master realm with
// the given issuer but hides it from the first lookup, so EnsureMaster takes
// the create path and loses with the given constraint violation.
func racing(t *testing.T, base, storedIssuer string, collision error) (*service.RealmService, *racingRepo) {
	t.Helper()

	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	seeded, err := realm.New("master", "Master", parsed)
	if err != nil {
		t.Fatalf("building the master realm: %v", err)
	}

	// Rehydrate rather than New: the stored issuer has to be able to differ
	// from what this base derives, and the aggregate has no issuer setter.
	stored, err := realm.Rehydrate(
		seeded.ID(), seeded.Slug(), seeded.DisplayName(), storedIssuer,
		seeded.Status(), seeded.CreatedAt(), seeded.UpdatedAt(),
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	repo := &racingRepo{fakeRepo: newFakeRepo(), hideFirstLookup: true, collision: collision}
	repo.bySlug[stored.Slug()] = stored
	repo.byID[stored.ID()] = stored

	return service.NewRealmService(fakeStore{repo: repo}, parsed), repo
}

// Two replicas boot against the same fresh database, both find no master
// realm, and both insert. The loser must adopt the winner's row rather than
// fail the boot: on a first rollout every replica is a loser except one, and
// the whole installation would crash-loop.
func TestEnsureMasterAdoptsTheRowAnotherReplicaSeeded(t *testing.T) {
	for named, collision := range seedCollisions {
		t.Run(named, func(t *testing.T) {
			svc, repo := racing(t, "https://idp.example.com", "https://idp.example.com/realms/master", collision)

			found, err := svc.EnsureMaster(t.Context(), false)
			if err != nil {
				t.Fatalf("losing the seed race must not fail the boot: %v", err)
			}

			if found.ID() != repo.bySlug["master"].ID() {
				t.Errorf("the realm the other replica seeded must be the one returned, got %s", found.ID())
			}
		})
	}
}

// The adopted row goes through exactly the reconciliation a row found by the
// first lookup does. Asserting both polarities is what proves the two paths
// converged rather than the recovery having been bolted on with a bare return.
func TestTheAdoptedRowIsReconciledLikeAnyOther(t *testing.T) {
	for named, collision := range seedCollisions {
		t.Run(named+"/production refuses a divergent issuer", func(t *testing.T) {
			assertAdoptedRowRefusedInProduction(t, collision)
		})

		t.Run(named+"/development rewrites a divergent issuer", func(t *testing.T) {
			assertAdoptedRowRewrittenInDevelopment(t, collision)
		})
	}
}

func assertAdoptedRowRefusedInProduction(t *testing.T, collision error) {
	t.Helper()

	svc, repo := racing(
		t, "https://idp.moved.example.com", "https://idp.example.com/realms/master", collision,
	)

	_, err := svc.EnsureMaster(t.Context(), false)
	if err == nil {
		t.Fatal("a divergent issuer must refuse the boot outside development, adopted row or not")
	}

	// Named explicitly, or this passes on the seed race failing rather than on
	// the reconciliation refusing, which is the whole point.
	if errors.Is(err, collision) || !strings.Contains(err.Error(), "idp.moved.example.com/realms/master") {
		t.Errorf("the refusal must be the issuer check, got %v", err)
	}

	if len(repo.reissued) != 0 {
		t.Error("production must not rewrite the issuer")
	}
}

func assertAdoptedRowRewrittenInDevelopment(t *testing.T, collision error) {
	t.Helper()

	svc, repo := racing(
		t, "https://idp.moved.example.com", "https://idp.example.com/realms/master", collision,
	)

	r, err := svc.EnsureMaster(t.Context(), true)
	if err != nil {
		t.Fatalf("development must rewrite rather than refuse, got %v", err)
	}

	if want := "https://idp.moved.example.com/realms/master"; r.Issuer() != want {
		t.Errorf("issuer: want %q, got %q", want, r.Issuer())
	}

	if len(repo.reissued) != 1 {
		t.Error("development must go through Reissue")
	}
}

// The polarity of this pair is the point: asserting only that production
// refuses would pass with the condition inverted.
func TestEnsureMasterRefusesADivergentIssuerOutsideDevelopment(t *testing.T) {
	svc, repo := newService(t)

	seeded, err := svc.EnsureMaster(t.Context(), false)
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	moved, _ := url.Parse("https://idp.moved.example.com")
	relocated := service.NewRealmService(fakeStore{repo: repo}, moved)

	if _, err := relocated.EnsureMaster(t.Context(), false); err == nil {
		t.Fatal("a divergent issuer must refuse the boot outside development")
	}

	if len(repo.reissued) != 0 {
		t.Error("production must not rewrite the issuer")
	}

	if repo.bySlug["master"].Issuer() != seeded.Issuer() {
		t.Error("the stored issuer must be left untouched")
	}
}

func TestEnsureMasterRewritesADivergentIssuerInDevelopment(t *testing.T) {
	svc, repo := newService(t)

	if _, err := svc.EnsureMaster(t.Context(), false); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	moved, _ := url.Parse("https://idp.moved.example.com")
	relocated := service.NewRealmService(fakeStore{repo: repo}, moved)

	r, err := relocated.EnsureMaster(t.Context(), true)
	if err != nil {
		t.Fatalf("development must rewrite rather than refuse, got %v", err)
	}

	if want := "https://idp.moved.example.com/realms/master"; r.Issuer() != want {
		t.Errorf("issuer: want %q, got %q", want, r.Issuer())
	}

	if len(repo.reissued) != 1 {
		t.Error("development must go through Reissue")
	}
}

// An operator who disabled the master realm did so on purpose. A boot step
// that quietly re-enabled it would undo a deliberate act with no trace.
func TestEnsureMasterLeavesTheStatusAlone(t *testing.T) {
	svc, repo := newService(t)

	if _, err := svc.EnsureMaster(t.Context(), false); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	stored := repo.bySlug["master"]
	if err := stored.SetStatus(realm.StatusDisabled); err != nil {
		t.Fatalf("disabling: %v", err)
	}

	r, err := svc.EnsureMaster(t.Context(), false)
	if err != nil {
		t.Fatalf("seeding again: %v", err)
	}

	if r.Status() != realm.StatusDisabled {
		t.Error("the seed must not re-enable a realm an operator disabled")
	}
}

func TestListClampsTheLimit(t *testing.T) {
	svc, repo := newService(t)

	for _, given := range []int{0, -5} {
		if _, err := svc.List(t.Context(), service.RealmQuery{Limit: given}); err != nil {
			t.Fatalf("listing: %v", err)
		}

		if repo.lastQuery.Limit != 50 {
			t.Errorf("limit %d: want the default 50, got %d", given, repo.lastQuery.Limit)
		}
	}

	if _, err := svc.List(t.Context(), service.RealmQuery{Limit: 5000}); err != nil {
		t.Fatalf("listing: %v", err)
	}

	if repo.lastQuery.Limit != 500 {
		t.Errorf("want the ceiling 500, got %d", repo.lastQuery.Limit)
	}
}

// Listing tombstones by default is not what any caller wants, and an explicit
// filter can still ask for them.
func TestListExcludesArchivedUnlessAsked(t *testing.T) {
	svc, repo := newService(t)

	if _, err := svc.List(t.Context(), service.RealmQuery{}); err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(repo.lastQuery.Status) != 2 {
		t.Fatalf("want active and disabled, got %v", repo.lastQuery.Status)
	}

	for _, status := range repo.lastQuery.Status {
		if status == realm.StatusArchived {
			t.Error("archived must not be listed by default")
		}
	}

	asked := service.RealmQuery{Status: []realm.Status{realm.StatusArchived}}
	if _, err := svc.List(t.Context(), asked); err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(repo.lastQuery.Status) != 1 || repo.lastQuery.Status[0] != realm.StatusArchived {
		t.Errorf("an explicit filter must be honoured, got %v", repo.lastQuery.Status)
	}
}

func TestArchiveMovesTheStatus(t *testing.T) {
	svc, repo := newService(t)

	created, err := svc.Create(t.Context(), "acme", "Acme")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := svc.Archive(t.Context(), created.ID()); err != nil {
		t.Fatalf("archiving: %v", err)
	}

	if repo.byID[created.ID()].Status() != realm.StatusArchived {
		t.Error("archive must set the status rather than delete the row")
	}
}
