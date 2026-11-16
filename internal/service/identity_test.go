package service_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/service"
)

const testPassword = "correct horse battery staple"

type identityFixture struct {
	identities  *service.IdentityService
	credentials *service.CredentialService
	store       *fakeStore
	hasher      *fakeHasher
}

func newIdentityFixture() *identityFixture {
	probe := &txProbe{}
	store := &fakeStore{
		repo:        newFakeRepo(),
		keys:        newFakeKeys(),
		identities:  newFakeIdentities(),
		credentials: newFakeCredentials(),
		tx:          probe,
	}
	hasher := &fakeHasher{tx: probe}
	credentials := service.NewCredentialService(store, hasher)

	return &identityFixture{
		identities:  service.NewIdentityService(store, credentials),
		credentials: credentials,
		store:       store,
		hasher:      hasher,
	}
}

func (f *identityFixture) create(t *testing.T, realmID uuid.UUID, email, username string) *identity.Identity {
	t.Helper()

	created, err := f.identities.Create(t.Context(), realmID, email, username)
	if err != nil {
		t.Fatalf("creating %s: %v", email, err)
	}

	return created
}

func (f *identityFixture) createWithPassword(
	t *testing.T, realmID uuid.UUID, email, username string, mustChange bool,
) *identity.Identity {
	t.Helper()

	created, err := f.identities.CreateWithPassword(t.Context(), realmID, email, username, testPassword, mustChange)
	if err != nil {
		t.Fatalf("creating %s: %v", email, err)
	}

	return created
}

func TestIdentityCreateNormalisesAndStores(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	created, err := f.identities.Create(t.Context(), realmID, "  Ana@Example.COM ", "Ana.Silva")
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if created.Email() != "ana@example.com" || created.Username() != "ana.silva" {
		t.Errorf("want the normal forms, got %q and %q", created.Email(), created.Username())
	}

	if created.Status() != identity.StatusActive {
		t.Errorf("want active, got %q", created.Status())
	}

	stored, ok := f.store.identities.rows[created.ID()]
	if !ok {
		t.Fatal("the identity must reach the repository")
	}

	if stored.RealmID() != realmID || stored.Email() != "ana@example.com" {
		t.Errorf("stored realm %s and email %q", stored.RealmID(), stored.Email())
	}
}

func TestIdentityCreateRefusesWhatTheAggregateRefuses(t *testing.T) {
	cases := []struct {
		name     string
		realmID  uuid.UUID
		email    string
		username string
		want     error
	}{
		{"no realm", uuid.Nil, "ana@example.com", "ana", identity.ErrRealmInvalid},
		{"empty email", uuid.New(), "", "ana", identity.ErrEmailInvalid},
		{"malformed email", uuid.New(), "ana@@example.com", "ana", identity.ErrEmailInvalid},
		{"username with an at", uuid.New(), "ana@example.com", "ana@home", identity.ErrUsernameInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFixture()

			if _, err := f.identities.Create(t.Context(), tc.realmID, tc.email, tc.username); !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}

			if len(f.store.identities.rows) != 0 {
				t.Error("a refused identity must not reach the repository")
			}
		})
	}
}

func TestIdentityCreateRefusesATakenEmail(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	f.create(t, realmID, "ana@example.com", "ana")

	// Different case on purpose: uniqueness is over the normal form.
	_, err := f.identities.Create(t.Context(), realmID, "ANA@example.com", "other")
	if !errors.Is(err, identity.ErrEmailTaken) {
		t.Errorf("want ErrEmailTaken, got %v", err)
	}
}

func TestIdentityCreateRefusesATakenUsername(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	f.create(t, realmID, "ana@example.com", "ana")

	_, err := f.identities.Create(t.Context(), realmID, "other@example.com", "Ana")
	if !errors.Is(err, identity.ErrUsernameTaken) {
		t.Errorf("want ErrUsernameTaken, got %v", err)
	}
}

func TestIdentitiesWithoutUsernameDoNotCollide(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	f.create(t, realmID, "ana@example.com", "")

	if _, err := f.identities.Create(t.Context(), realmID, "bia@example.com", ""); err != nil {
		t.Errorf("want no error, got %v", err)
	}
}

func TestTheSameEmailAndUsernameLiveInTwoRealms(t *testing.T) {
	f := newIdentityFixture()

	first := f.create(t, uuid.New(), "ana@example.com", "ana")

	second, err := f.identities.Create(t.Context(), uuid.New(), "ana@example.com", "ana")
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if first.ID() == second.ID() {
		t.Error("want two identities")
	}
}

func TestCreateWithPasswordWritesTheIdentityAndItsCredential(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	created := f.createWithPassword(t, realmID, "ana@example.com", "ana", true)

	if _, ok := f.store.identities.rows[created.ID()]; !ok {
		t.Fatal("the identity must reach the repository")
	}

	stored, err := f.store.credentials.Find(t.Context(), realmID, created.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading the credential: %v", err)
	}

	if !stored.MustChange() {
		t.Error("must-change must be carried to the credential")
	}

	if ok, _, _ := f.hasher.Verify(testPassword, stored.Hash()); !ok {
		t.Error("the stored hash must be the one of the given password")
	}
}

func TestCreateWithPasswordWritesNeitherWhenTheCredentialInsertFails(t *testing.T) {
	f := newIdentityFixture()
	boom := errors.New("boom")

	f.store.credentials.failNextCreate = boom

	_, err := f.identities.CreateWithPassword(t.Context(), uuid.New(), "ana@example.com", "ana", testPassword, false)
	if !errors.Is(err, boom) {
		t.Fatalf("want the insert error, got %v", err)
	}

	if len(f.store.identities.rows) != 0 {
		t.Error("the identity must be rolled back with its credential")
	}

	if len(f.store.credentials.rows) != 0 {
		t.Error("want no credential written")
	}
}

func TestCreateWithPasswordWritesNeitherWhenTheIdentityInsertFails(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	f.create(t, realmID, "ana@example.com", "ana")

	_, err := f.identities.CreateWithPassword(t.Context(), realmID, "ana@example.com", "other", testPassword, false)
	if !errors.Is(err, identity.ErrEmailTaken) {
		t.Fatalf("want ErrEmailTaken, got %v", err)
	}

	if len(f.store.credentials.rows) != 0 {
		t.Error("want no credential written")
	}
}

// The split between prepare and persist exists so no transaction is ever held
// across a password hash.
func TestCreateWithPasswordHashesBeforeTheTransactionOpens(t *testing.T) {
	f := newIdentityFixture()

	f.createWithPassword(t, uuid.New(), "ana@example.com", "ana", false)

	if f.hasher.hashCalls != 1 {
		t.Errorf("want one hash, got %d", f.hasher.hashCalls)
	}

	if f.hasher.callsInTx != 0 {
		t.Errorf("want no hashing inside the transaction, got %d calls", f.hasher.callsInTx)
	}
}

func TestCreateWithPasswordRefusesAnUnacceptablePassword(t *testing.T) {
	f := newIdentityFixture()

	_, err := f.identities.CreateWithPassword(t.Context(), uuid.New(), "ana@example.com", "ana", "", false)
	if !errors.Is(err, credential.ErrPasswordInvalid) {
		t.Fatalf("want ErrPasswordInvalid, got %v", err)
	}

	if len(f.store.identities.rows) != 0 {
		t.Error("an identity meant to have a password must not be written without it")
	}
}

func TestCreateWithPasswordDoesNotHashForAnIdentityItWillRefuse(t *testing.T) {
	f := newIdentityFixture()

	_, err := f.identities.CreateWithPassword(t.Context(), uuid.New(), "not an email", "ana", testPassword, false)
	if !errors.Is(err, identity.ErrEmailInvalid) {
		t.Fatalf("want ErrEmailInvalid, got %v", err)
	}

	if f.hasher.hashCalls != 0 {
		t.Errorf("want no hash, got %d", f.hasher.hashCalls)
	}
}

func TestIdentityFindByIDIsScopedToTheRealm(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	created := f.create(t, realmID, "ana@example.com", "ana")

	found, err := f.identities.FindByID(t.Context(), realmID, created.ID())
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if found.ID() != created.ID() {
		t.Errorf("want %s, got %s", created.ID(), found.ID())
	}

	if _, err := f.identities.FindByID(t.Context(), uuid.New(), created.ID()); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("another realm: want ErrNotFound, got %v", err)
	}
}

func TestFindByLoginResolvesEmailAndUsername(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	created := f.create(t, realmID, "ana@example.com", "ana.silva")

	for _, login := range []string{"ana@example.com", " Ana@Example.COM ", "ana.silva", "ANA.Silva "} {
		found, err := f.identities.FindByLogin(t.Context(), realmID, login)
		if err != nil {
			t.Errorf("login %q: want no error, got %v", login, err)

			continue
		}

		if found.ID() != created.ID() {
			t.Errorf("login %q: want %s, got %s", login, created.ID(), found.ID())
		}
	}
}

func TestFindByLoginReportsNotFound(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	f.create(t, realmID, "ana@example.com", "ana")
	// No username: an empty login must not resolve to this one.
	f.create(t, realmID, "bia@example.com", "")

	cases := map[string]string{
		"unknown email":      "nobody@example.com",
		"unknown username":   "nobody",
		"malformed email":    "ana@@example.com",
		"malformed username": "not a username",
		"empty":              "",
		"blank":              "   ",
	}

	for name, login := range cases {
		if _, err := f.identities.FindByLogin(t.Context(), realmID, login); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("%s: want ErrNotFound, got %v", name, err)
		}
	}

	if _, err := f.identities.FindByLogin(t.Context(), uuid.New(), "ana"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("another realm: want ErrNotFound, got %v", err)
	}
}

func TestIdentityListClampsTheLimit(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	for given, want := range map[int]int{0: 50, -5: 50, 20: 20, 500: 500, 5000: 500} {
		if _, err := f.identities.List(t.Context(), realmID, service.IdentityQuery{Limit: given}); err != nil {
			t.Fatalf("listing: %v", err)
		}

		if got := f.store.identities.lastQuery.Limit; got != want {
			t.Errorf("limit %d: want %d, got %d", given, want, got)
		}
	}
}

func TestIdentityListPassesTheRealmAndTheFilter(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	after := uuid.New()

	q := service.IdentityQuery{After: after, Status: []identity.Status{identity.StatusDisabled}}
	if _, err := f.identities.List(t.Context(), realmID, q); err != nil {
		t.Fatalf("listing: %v", err)
	}

	if f.store.identities.lastRealm != realmID {
		t.Errorf("want realm %s, got %s", realmID, f.store.identities.lastRealm)
	}

	got := f.store.identities.lastQuery
	if got.After != after || len(got.Status) != 1 || got.Status[0] != identity.StatusDisabled {
		t.Errorf("the cursor and the filter must be passed through, got %+v", got)
	}
}

func TestIdentitySetStatusPersists(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	created := f.create(t, realmID, "ana@example.com", "ana")

	if err := f.identities.SetStatus(t.Context(), realmID, created.ID(), identity.StatusDisabled); err != nil {
		t.Fatalf("disabling: %v", err)
	}

	if got := f.store.identities.rows[created.ID()].Status(); got != identity.StatusDisabled {
		t.Errorf("want disabled, got %q", got)
	}

	if err := f.identities.SetStatus(t.Context(), realmID, created.ID(), identity.StatusActive); err != nil {
		t.Fatalf("enabling: %v", err)
	}

	if got := f.store.identities.rows[created.ID()].Status(); got != identity.StatusActive {
		t.Errorf("want active, got %q", got)
	}
}

func TestIdentitySetStatusIsScopedToTheRealm(t *testing.T) {
	f := newIdentityFixture()

	created := f.create(t, uuid.New(), "ana@example.com", "ana")

	err := f.identities.SetStatus(t.Context(), uuid.New(), created.ID(), identity.StatusDisabled)
	if !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	if got := f.store.identities.rows[created.ID()].Status(); got != identity.StatusActive {
		t.Errorf("an identity of another realm must be left alone, got %q", got)
	}
}

func TestIdentitySetStatusRefusesAnUnknownIdentityOrStatus(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	err := f.identities.SetStatus(t.Context(), realmID, uuid.New(), identity.StatusDisabled)
	if !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}

	created := f.create(t, realmID, "ana@example.com", "ana")

	err = f.identities.SetStatus(t.Context(), realmID, created.ID(), identity.Status("archived"))
	if !errors.Is(err, identity.ErrStatusInvalid) {
		t.Errorf("want ErrStatusInvalid, got %v", err)
	}

	if got := f.store.identities.rows[created.ID()].Status(); got != identity.StatusActive {
		t.Errorf("a refused status must not be written, got %q", got)
	}
}

// fakeIdentities mimics the two unique indexes and the realm scoping. It stores
// and returns copies, like a database: mutating an aggregate changes nothing
// until Update is called.
type fakeIdentities struct {
	rows map[uuid.UUID]*identity.Identity

	lastRealm uuid.UUID
	lastQuery service.IdentityQuery
}

func newFakeIdentities() *fakeIdentities {
	return &fakeIdentities{rows: map[uuid.UUID]*identity.Identity{}}
}

// snapshot returns what undoes every write made after it. Nil-safe, so a store
// built without identities still runs transactions.
func (f *fakeIdentities) snapshot() func() {
	if f == nil {
		return func() {}
	}

	saved := maps.Clone(f.rows)

	return func() { f.rows = saved }
}

func (f *fakeIdentities) Create(_ context.Context, i *identity.Identity) error {
	for _, existing := range f.rows {
		if existing.RealmID() != i.RealmID() {
			continue
		}

		if existing.Email() == i.Email() {
			return identity.ErrEmailTaken
		}

		if i.HasUsername() && existing.Username() == i.Username() {
			return identity.ErrUsernameTaken
		}
	}

	f.rows[i.ID()] = copyIdentity(i)

	return nil
}

func (f *fakeIdentities) FindByID(_ context.Context, realmID, id uuid.UUID) (*identity.Identity, error) {
	return f.find(func(i *identity.Identity) bool { return i.RealmID() == realmID && i.ID() == id })
}

func (f *fakeIdentities) FindByEmail(_ context.Context, realmID uuid.UUID, email string) (*identity.Identity, error) {
	return f.find(func(i *identity.Identity) bool { return i.RealmID() == realmID && i.Email() == email })
}

func (f *fakeIdentities) FindByUsername(
	_ context.Context, realmID uuid.UUID, username string,
) (*identity.Identity, error) {
	return f.find(func(i *identity.Identity) bool { return i.RealmID() == realmID && i.Username() == username })
}

func (f *fakeIdentities) find(match func(*identity.Identity) bool) (*identity.Identity, error) {
	for _, candidate := range f.rows {
		if match(candidate) {
			return copyIdentity(candidate), nil
		}
	}

	return nil, identity.ErrNotFound
}

func (f *fakeIdentities) List(
	_ context.Context, realmID uuid.UUID, q service.IdentityQuery,
) ([]*identity.Identity, error) {
	f.lastRealm = realmID
	f.lastQuery = q

	return nil, nil
}

func (f *fakeIdentities) Update(_ context.Context, i *identity.Identity) error {
	stored, ok := f.rows[i.ID()]
	if !ok || stored.RealmID() != i.RealmID() {
		return identity.ErrNotFound
	}

	f.rows[i.ID()] = copyIdentity(i)

	return nil
}

func copyIdentity(i *identity.Identity) *identity.Identity {
	copied, err := identity.Rehydrate(
		i.ID(), i.RealmID(), i.Email(), i.Username(), i.Status(), i.CreatedAt(), i.UpdatedAt(),
	)
	if err != nil {
		panic(err)
	}

	return copied
}
