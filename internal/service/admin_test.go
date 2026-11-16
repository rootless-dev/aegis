package service_test

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/admin"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/service"
)

const (
	rootEmail     = "root@example.com"
	rootUsername  = "root"
	otherPassword = "a different password"
)

type adminFixture struct {
	*identityFixture

	admins *service.AdminService
	master *realm.Realm
	acme   *realm.Realm
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()

	base := newIdentityFixture()
	base.store.sessions = newFakeSessions()
	base.store.sessions.tx = base.store.tx
	base.store.adminGrants = newFakeAdminGrants(base.store.identities, base.store.credentials)

	f := &adminFixture{
		identityFixture: base,
		admins:          service.NewAdminService(base.store, base.identities, base.credentials),
		master:          newTestRealm(t, "master", passwordGrantSettings()),
		acme:            newTestRealm(t, "acme", passwordGrantSettings()),
	}

	for _, r := range []*realm.Realm{f.master, f.acme} {
		if err := base.store.repo.Create(t.Context(), r); err != nil {
			t.Fatalf("storing realm %s: %v", r.Slug(), err)
		}
	}

	return f
}

// administrator stores one able to sign in, with a password that is not
// marked for replacement.
func (f *adminFixture) administrator(t *testing.T, email, username string) *identity.Identity {
	t.Helper()

	created := f.createWithPassword(t, f.master.ID(), email, username, false)
	f.grantGlobal(t, created)

	return created
}

func (f *adminFixture) grantGlobal(t *testing.T, to *identity.Identity) {
	t.Helper()

	if _, err := f.admins.GrantGlobal(t.Context(), to.ID()); err != nil {
		t.Fatalf("granting %s: %v", to.Email(), err)
	}
}

func (f *adminFixture) disable(t *testing.T, who *identity.Identity) {
	t.Helper()

	if err := f.identities.SetStatus(t.Context(), f.master.ID(), who.ID(), identity.StatusDisabled); err != nil {
		t.Fatalf("disabling %s: %v", who.Email(), err)
	}
}

func (f *adminFixture) openSession(t *testing.T, of *identity.Identity) uuid.UUID {
	t.Helper()

	opened, err := session.New(f.master.ID(), of.ID(), time.Now(), time.Hour, testClientIP, testUserAgent)
	if err != nil {
		t.Fatalf("building a session: %v", err)
	}

	if err := f.store.sessions.Create(t.Context(), opened); err != nil {
		t.Fatalf("storing a session: %v", err)
	}

	return opened.ID()
}

func (f *adminFixture) grantsOf(t *testing.T, of *identity.Identity) []*admin.Grant {
	t.Helper()

	held, err := f.admins.ListByIdentity(t.Context(), of.ID())
	if err != nil {
		t.Fatalf("listing the grants of %s: %v", of.Email(), err)
	}

	return held
}

func (f *adminFixture) stored(t *testing.T, id uuid.UUID) *identity.Identity {
	t.Helper()

	found, err := f.store.identities.FindByID(t.Context(), f.master.ID(), id)
	if err != nil {
		t.Fatalf("reading identity %s: %v", id, err)
	}

	return found
}

// assertTemporaryAdministrator checks everything Bootstrap and
// ResetAdministrator promise about the identity they hand back.
func (f *adminFixture) assertTemporaryAdministrator(t *testing.T, who *identity.Identity, password string) {
	t.Helper()

	if got := f.stored(t, who.ID()).Status(); got != identity.StatusActive {
		t.Errorf("status: want active, got %s", got)
	}

	stored := f.storedPassword(t, who)

	if ok, _, _ := f.hasher.Verify(password, stored.Hash()); !ok {
		t.Error("the stored hash must verify the supplied password")
	}

	if !stored.MustChange() {
		t.Error("the password must be marked for replacement")
	}

	held := f.grantsOf(t, who)
	if n := countGlobal(held); n != 1 {
		t.Errorf("want exactly one global grant, got %d of %d grants", n, len(held))
	}

	if err := f.admins.Authorize(t.Context(), who.ID(), f.acme.ID()); err != nil {
		t.Errorf("the administrator must be authorized everywhere, got %v", err)
	}

	if f.hasher.callsInTx != 0 {
		t.Errorf("want no hashing inside a transaction, got %d calls", f.hasher.callsInTx)
	}
}

func (f *adminFixture) assertEmpty(t *testing.T) {
	t.Helper()

	identities, credentials, grants := f.rowCounts()
	if identities+credentials+grants != 0 {
		t.Errorf("want nothing stored, got %d identities, %d credentials, %d grants", identities, credentials, grants)
	}
}

func (f *adminFixture) rowCounts() (identities, credentials, grants int) {
	return len(f.store.identities.rows), len(f.store.credentials.rows), len(f.store.adminGrants.rows)
}

func countGlobal(grants []*admin.Grant) int {
	n := 0

	for _, g := range grants {
		if g.Global() {
			n++
		}
	}

	return n
}

func TestGrantGlobal(t *testing.T) {
	f := newAdminFixture(t)
	ana := f.create(t, f.master.ID(), "ana@example.com", "ana")

	granted, err := f.admins.GrantGlobal(t.Context(), ana.ID())
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if !granted.Global() || granted.RealmID() != f.master.ID() || granted.IdentityID() != ana.ID() {
		t.Errorf("want a global grant of the master realm held by ana, got %+v", granted)
	}

	if _, err := f.admins.GrantGlobal(t.Context(), ana.ID()); !errors.Is(err, admin.ErrExists) {
		t.Errorf("a second global grant: want ErrExists, got %v", err)
	}
}

func TestGrantRealm(t *testing.T) {
	f := newAdminFixture(t)
	ana := f.create(t, f.master.ID(), "ana@example.com", "ana")

	granted, err := f.admins.GrantRealm(t.Context(), ana.ID(), f.acme.ID())
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if granted.Global() || granted.TargetRealmID() != f.acme.ID() || granted.RealmID() != f.master.ID() {
		t.Errorf("want a grant of the master realm naming acme, got %+v", granted)
	}

	if _, err := f.admins.GrantRealm(t.Context(), ana.ID(), f.acme.ID()); !errors.Is(err, admin.ErrExists) {
		t.Errorf("the same grant twice: want ErrExists, got %v", err)
	}

	if _, err := f.admins.GrantGlobal(t.Context(), ana.ID()); err != nil {
		t.Errorf("a global grant beside a realm grant: want no error, got %v", err)
	}

	if _, err := f.admins.GrantRealm(t.Context(), ana.ID(), uuid.New()); !errors.Is(err, realm.ErrNotFound) {
		t.Errorf("an unknown target: want realm.ErrNotFound, got %v", err)
	}

	if _, err := f.admins.GrantRealm(t.Context(), ana.ID(), uuid.Nil); !errors.Is(err, admin.ErrTargetInvalid) {
		t.Errorf("a nil target: want ErrTargetInvalid, got %v", err)
	}
}

func TestGrantRefusesAnIdentityOutsideTheMasterRealm(t *testing.T) {
	f := newAdminFixture(t)
	outsider := f.create(t, f.acme.ID(), "ana@example.com", "ana")

	for name, id := range map[string]uuid.UUID{"another realm": outsider.ID(), "unknown": uuid.New()} {
		if _, err := f.admins.GrantGlobal(t.Context(), id); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("%s, global: want identity.ErrNotFound, got %v", name, err)
		}

		if _, err := f.admins.GrantRealm(t.Context(), id, f.acme.ID()); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("%s, realm: want identity.ErrNotFound, got %v", name, err)
		}
	}

	if n := len(f.store.adminGrants.rows); n != 0 {
		t.Errorf("want no grant stored, got %d", n)
	}
}

func TestRevoke(t *testing.T) {
	f := newAdminFixture(t)
	ana := f.create(t, f.master.ID(), "ana@example.com", "ana")
	f.grantGlobal(t, ana)

	if _, err := f.admins.GrantRealm(t.Context(), ana.ID(), f.acme.ID()); err != nil {
		t.Fatalf("granting acme: %v", err)
	}

	if err := f.admins.Revoke(t.Context(), ana.ID(), f.master.ID()); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("a grant that is not held: want ErrNotFound, got %v", err)
	}

	if err := f.admins.Revoke(t.Context(), ana.ID(), uuid.Nil); err != nil {
		t.Fatalf("revoking the global grant: %v", err)
	}

	held := f.grantsOf(t, ana)
	if len(held) != 1 || held[0].TargetRealmID() != f.acme.ID() {
		t.Fatalf("want only the acme grant left, got %d grants", len(held))
	}

	if err := f.admins.Revoke(t.Context(), ana.ID(), uuid.Nil); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("revoking twice: want ErrNotFound, got %v", err)
	}

	// The last grant of the installation goes like any other.
	if err := f.admins.Revoke(t.Context(), ana.ID(), f.acme.ID()); err != nil {
		t.Errorf("revoking the last grant: want no error, got %v", err)
	}

	if err := f.admins.Revoke(t.Context(), uuid.New(), uuid.Nil); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("an unknown identity: want ErrNotFound, got %v", err)
	}
}

func TestListReturnsEveryGrantOldestFirst(t *testing.T) {
	f := newAdminFixture(t)
	ana := f.create(t, f.master.ID(), "ana@example.com", "ana")
	bob := f.create(t, f.master.ID(), "bob@example.com", "bob")

	first, err := f.admins.GrantRealm(t.Context(), bob.ID(), f.acme.ID())
	if err != nil {
		t.Fatalf("granting bob: %v", err)
	}

	second, err := f.admins.GrantGlobal(t.Context(), ana.ID())
	if err != nil {
		t.Fatalf("granting ana: %v", err)
	}

	listed, err := f.admins.List(t.Context())
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(listed) != 2 || listed[0].ID() != first.ID() || listed[1].ID() != second.ID() {
		t.Errorf("want both grants in the order they were made, got %d", len(listed))
	}

	if held := f.grantsOf(t, bob); len(held) != 1 || held[0].ID() != first.ID() {
		t.Errorf("want only bob's grant listed for bob, got %d", len(held))
	}
}

func TestAuthorize(t *testing.T) {
	f := newAdminFixture(t)

	global := f.create(t, f.master.ID(), "global@example.com", "")
	f.grantGlobal(t, global)

	scoped := f.create(t, f.master.ID(), "scoped@example.com", "")
	if _, err := f.admins.GrantRealm(t.Context(), scoped.ID(), f.acme.ID()); err != nil {
		t.Fatalf("granting acme: %v", err)
	}

	disabled := f.create(t, f.master.ID(), "disabled@example.com", "")
	f.grantGlobal(t, disabled)
	f.disable(t, disabled)

	ungranted := f.create(t, f.master.ID(), "nobody@example.com", "")
	outsider := f.create(t, f.acme.ID(), "outsider@example.com", "")

	cases := []struct {
		name     string
		identity uuid.UUID
		target   uuid.UUID
		allowed  bool
	}{
		{"global grant, any realm", global.ID(), f.acme.ID(), true},
		{"global grant, the master realm", global.ID(), f.master.ID(), true},
		{"global grant, a realm that does not exist", global.ID(), uuid.New(), true},
		{"global grant, nil target", global.ID(), uuid.Nil, false},
		{"realm grant, that realm", scoped.ID(), f.acme.ID(), true},
		{"realm grant, another realm", scoped.ID(), f.master.ID(), false},
		{"realm grant, nil target", scoped.ID(), uuid.Nil, false},
		{"disabled identity", disabled.ID(), f.acme.ID(), false},
		{"no grant", ungranted.ID(), f.acme.ID(), false},
		{"identity of another realm", outsider.ID(), f.acme.ID(), false},
		{"unknown identity", uuid.New(), f.acme.ID(), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := f.admins.Authorize(t.Context(), tc.identity, tc.target)

			if tc.allowed && err != nil {
				t.Errorf("want authorized, got %v", err)
			}

			// Compared, not errors.Is: a wrapped cause would tell the caller
			// which rule refused.
			if !tc.allowed && err != admin.ErrNotAuthorized {
				t.Errorf("want exactly ErrNotAuthorized, got %v", err)
			}
		})
	}
}

func TestBootstrapCreatesTheFirstAdministrator(t *testing.T) {
	f := newAdminFixture(t)

	outcome, err := f.admins.Bootstrap(t.Context(), " Root@Example.com ", rootUsername, testPassword)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if !outcome.Created || outcome.Completed || outcome.Administrator == nil {
		t.Fatalf("want a created administrator, got %+v", outcome)
	}

	root := outcome.Administrator
	if root.Email() != rootEmail || root.Username() != rootUsername || root.RealmID() != f.master.ID() {
		t.Errorf("want %s in the master realm, got %s (%s)", rootEmail, root.Email(), root.Username())
	}

	f.assertTemporaryAdministrator(t, root, testPassword)

	if f.store.tx.entered != 1 {
		t.Errorf("want one transaction, got %d", f.store.tx.entered)
	}

	if f.hasher.hashCalls != 1 {
		t.Errorf("want one hash, got %d", f.hasher.hashCalls)
	}
}

func TestBootstrapIsNotAPasswordReset(t *testing.T) {
	f := newAdminFixture(t)

	first, err := f.admins.Bootstrap(t.Context(), rootEmail, rootUsername, testPassword)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}

	hashes, transactions := f.hasher.hashCalls, f.store.tx.entered

	arguments := map[string][3]string{
		"same":               {rootEmail, rootUsername, testPassword},
		"different password": {rootEmail, rootUsername, otherPassword},
		"different email":    {"other@example.com", "other", otherPassword},
		"invalid":            {"not an email", "Not A Username", ""},
	}

	for name, args := range arguments {
		outcome, err := f.admins.Bootstrap(t.Context(), args[0], args[1], args[2])
		if err != nil {
			t.Fatalf("%s: want no error, got %v", name, err)
		}

		if outcome != (service.BootstrapOutcome{}) {
			t.Errorf("%s: want nothing done, got %+v", name, outcome)
		}
	}

	if f.hasher.hashCalls != hashes || f.store.tx.entered != transactions {
		t.Error("a later boot must hash nothing and open no transaction")
	}

	if n := len(f.store.identities.rows); n != 1 {
		t.Errorf("want one identity, got %d", n)
	}

	f.assertTemporaryAdministrator(t, first.Administrator, testPassword)
}

func TestBootstrapActsWhenNoAdministratorCanSignIn(t *testing.T) {
	cases := map[string]func(*testing.T, *adminFixture){
		"the only administrator is disabled": func(t *testing.T, f *adminFixture) {
			f.disable(t, f.administrator(t, "old@example.com", "old"))
		},
		"the only administrator has no password": func(t *testing.T, f *adminFixture) {
			f.grantGlobal(t, f.create(t, f.master.ID(), "old@example.com", "old"))
		},
		"the only grant names a realm": func(t *testing.T, f *adminFixture) {
			scoped := f.createWithPassword(t, f.master.ID(), "old@example.com", "old", false)
			if _, err := f.admins.GrantRealm(t.Context(), scoped.ID(), f.acme.ID()); err != nil {
				t.Fatalf("granting acme: %v", err)
			}
		},
	}

	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAdminFixture(t)
			arrange(t, f)

			outcome, err := f.admins.Bootstrap(t.Context(), rootEmail, rootUsername, testPassword)
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}

			if !outcome.Created {
				t.Fatalf("want a created administrator, got %+v", outcome)
			}

			f.assertTemporaryAdministrator(t, outcome.Administrator, testPassword)
		})
	}
}

func TestBootstrapCompletesAnIdentityHoldingTheEmail(t *testing.T) {
	cases := map[string]func(*testing.T, *adminFixture) *identity.Identity{
		"no password, no grant": func(t *testing.T, f *adminFixture) *identity.Identity {
			return f.create(t, f.master.ID(), rootEmail, "squatter")
		},
		"its own password": func(t *testing.T, f *adminFixture) *identity.Identity {
			return f.createWithPassword(t, f.master.ID(), rootEmail, "squatter", false)
		},
		"a disabled administrator": func(t *testing.T, f *adminFixture) *identity.Identity {
			old := f.administrator(t, rootEmail, "squatter")
			f.disable(t, old)

			return old
		},
	}

	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAdminFixture(t)
			squatter := arrange(t, f)
			live := f.openSession(t, squatter)

			outcome, err := f.admins.Bootstrap(t.Context(), rootEmail, rootUsername, otherPassword)
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}

			if outcome.Created || !outcome.Completed || outcome.Administrator == nil {
				t.Fatalf("want a completed administrator, got %+v", outcome)
			}

			if outcome.Administrator.ID() != squatter.ID() {
				t.Error("want the identity that held the email, got another one")
			}

			if got := f.stored(t, squatter.ID()).Username(); got != "squatter" {
				t.Errorf("the username must be left alone, got %q", got)
			}

			if n := len(f.store.identities.rows); n != 1 {
				t.Errorf("want no second identity, got %d", n)
			}

			f.assertTemporaryAdministrator(t, squatter, otherPassword)

			if ended, _ := f.store.sessions.Find(t.Context(), f.master.ID(), live); !ended.Ended() {
				t.Error("a session opened with the previous password must be ended")
			}
		})
	}
}

func TestBootstrapAdoptsTheAdministratorOfAReplicaThatWon(t *testing.T) {
	// What the loser supplied: the winner's email, or only its username.
	cases := map[string][2]string{
		"email":    {rootEmail, "other"},
		"username": {"other@example.com", rootUsername},
	}

	for name, supplied := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAdminFixture(t)
			winner := f.administrator(t, rootEmail, rootUsername)
			hash := f.storedPassword(t, winner).Hash()

			// The loser looked before the winner committed.
			f.store.adminGrants.hideFirstCheck = true

			outcome, err := f.admins.Bootstrap(t.Context(), supplied[0], supplied[1], otherPassword)
			if err != nil {
				t.Fatalf("losing the race must not fail the boot, got %v", err)
			}

			if outcome != (service.BootstrapOutcome{}) {
				t.Errorf("want nothing done, got %+v", outcome)
			}

			if n := len(f.store.identities.rows); n != 1 {
				t.Errorf("want only the winner's identity, got %d", n)
			}

			if got := f.storedPassword(t, winner).Hash(); got != hash {
				t.Error("the winner's password must not be replaced")
			}
		})
	}
}

func TestBootstrapRefusesAUsernameHeldBySomeoneElse(t *testing.T) {
	f := newAdminFixture(t)
	holder := f.create(t, f.master.ID(), "someone@example.com", rootUsername)

	_, err := f.admins.Bootstrap(t.Context(), rootEmail, rootUsername, testPassword)
	if !errors.Is(err, identity.ErrUsernameTaken) {
		t.Fatalf("want ErrUsernameTaken, got %v", err)
	}

	if len(f.grantsOf(t, holder)) != 0 || len(f.store.credentials.rows) != 0 || len(f.store.identities.rows) != 1 {
		t.Error("the holder of the username must be left untouched and nothing else written")
	}
}

func TestBootstrapLeavesNothingBehindWhenItFailsMidway(t *testing.T) {
	boom := errors.New("fake: the database went away")

	cases := map[string]func(*adminFixture){
		"at the credential": func(f *adminFixture) { f.store.credentials.failNextCreate = boom },
		"at the grant":      func(f *adminFixture) { f.store.adminGrants.failNextCreate = boom },
	}

	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAdminFixture(t)
			arrange(f)

			if _, err := f.admins.Bootstrap(t.Context(), rootEmail, rootUsername, testPassword); !errors.Is(err, boom) {
				t.Fatalf("want the storage error, got %v", err)
			}

			f.assertEmpty(t)

			outcome, err := f.admins.Bootstrap(t.Context(), rootEmail, rootUsername, testPassword)
			if err != nil || !outcome.Created {
				t.Errorf("the next boot must succeed, got %+v, %v", outcome, err)
			}
		})
	}
}

func TestBootstrapReturnsTheDomainErrorForInvalidInput(t *testing.T) {
	cases := map[string]struct {
		email, username, password string
		want                      error
	}{
		"email":             {"not an email", rootUsername, testPassword, identity.ErrEmailInvalid},
		"username":          {rootEmail, "Not A Username", testPassword, identity.ErrUsernameInvalid},
		"empty password":    {rootEmail, rootUsername, "", credential.ErrPasswordInvalid},
		"oversize password": {rootEmail, rootUsername, string(make([]byte, 1025)), credential.ErrPasswordInvalid},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAdminFixture(t)

			if _, err := f.admins.Bootstrap(t.Context(), tc.email, tc.username, tc.password); !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}

			f.assertEmpty(t)
		})
	}
}

func TestBootstrapAcceptsAWeakPassword(t *testing.T) {
	f := newAdminFixture(t)

	outcome, err := f.admins.Bootstrap(t.Context(), rootEmail, "", "a")
	if err != nil || !outcome.Created {
		t.Fatalf("want a created administrator, got %+v, %v", outcome, err)
	}
}

func TestResetAdministratorCreatesWhenAbsent(t *testing.T) {
	f := newAdminFixture(t)
	f.administrator(t, "other@example.com", "other")

	root, err := f.admins.ResetAdministrator(t.Context(), rootEmail, rootUsername, testPassword)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if root.Email() != rootEmail || root.Username() != rootUsername {
		t.Errorf("want %s (%s), got %s (%s)", rootEmail, rootUsername, root.Email(), root.Username())
	}

	f.assertTemporaryAdministrator(t, root, testPassword)
}

func TestResetAdministratorResetsAnExistingOne(t *testing.T) {
	f := newAdminFixture(t)
	root := f.administrator(t, rootEmail, rootUsername)
	bystander := f.administrator(t, "other@example.com", "other")

	live, untouched := f.openSession(t, root), f.openSession(t, bystander)
	f.disable(t, root)

	reset, err := f.admins.ResetAdministrator(t.Context(), " ROOT@example.com ", "ignored", otherPassword)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if reset.ID() != root.ID() || reset.Status() != identity.StatusActive {
		t.Errorf("want the same identity back, active; got %s, %s", reset.ID(), reset.Status())
	}

	if got := f.stored(t, root.ID()).Username(); got != rootUsername {
		t.Errorf("the username must be left alone, got %q", got)
	}

	f.assertTemporaryAdministrator(t, root, otherPassword)

	if ok, _, _ := f.hasher.Verify(testPassword, f.storedPassword(t, root).Hash()); ok {
		t.Error("the previous password must no longer verify")
	}

	ended, _ := f.store.sessions.Find(t.Context(), f.master.ID(), live)
	if !ended.Ended() || ended.EndReason() != session.EndRevoked {
		t.Errorf("the administrator's session must be revoked, got ended=%t reason=%q", ended.Ended(), ended.EndReason())
	}

	if kept, _ := f.store.sessions.Find(t.Context(), f.master.ID(), untouched); kept.Ended() {
		t.Error("another administrator's session must be left alone")
	}

	if f.storedPassword(t, bystander).MustChange() {
		t.Error("another administrator's password must be left alone")
	}
}

func TestResetAdministratorPromotesAnIdentityThatIsNotOne(t *testing.T) {
	f := newAdminFixture(t)
	ana := f.create(t, f.master.ID(), rootEmail, "ana")

	if _, err := f.admins.GrantRealm(t.Context(), ana.ID(), f.acme.ID()); err != nil {
		t.Fatalf("granting acme: %v", err)
	}

	promoted, err := f.admins.ResetAdministrator(t.Context(), rootEmail, "", testPassword)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if promoted.ID() != ana.ID() {
		t.Error("want the existing identity promoted, got another one")
	}

	f.assertTemporaryAdministrator(t, ana, testPassword)

	if n := len(f.grantsOf(t, ana)); n != 2 {
		t.Errorf("want the realm grant kept beside the global one, got %d grants", n)
	}
}

func TestResetAdministratorIsIdempotent(t *testing.T) {
	f := newAdminFixture(t)

	first, err := f.admins.ResetAdministrator(t.Context(), rootEmail, rootUsername, testPassword)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	second, err := f.admins.ResetAdministrator(t.Context(), rootEmail, rootUsername, testPassword)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if first.ID() != second.ID() {
		t.Error("want the same identity both times")
	}

	identities, credentials, grants := f.rowCounts()
	if identities != 1 || credentials != 1 || grants != 1 {
		t.Errorf("want one of each, got %d identities, %d credentials, %d grants", identities, credentials, grants)
	}

	f.assertTemporaryAdministrator(t, first, testPassword)
}

func TestResetAdministratorSurvivesAGrantAddedConcurrently(t *testing.T) {
	f := newAdminFixture(t)
	ana := f.create(t, f.master.ID(), rootEmail, "ana")
	f.store.adminGrants.failNextCreate = admin.ErrExists

	if _, err := f.admins.ResetAdministrator(t.Context(), rootEmail, "", testPassword); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	f.assertTemporaryAdministrator(t, ana, testPassword)
}

func TestResetAdministratorRollsBackWhenItFailsMidway(t *testing.T) {
	f := newAdminFixture(t)
	root := f.administrator(t, rootEmail, rootUsername)
	live := f.openSession(t, root)
	f.disable(t, root)

	if err := f.admins.Revoke(t.Context(), root.ID(), uuid.Nil); err != nil {
		t.Fatalf("revoking: %v", err)
	}

	boom := errors.New("fake: the database went away")
	f.store.adminGrants.failNextCreate = boom

	if _, err := f.admins.ResetAdministrator(t.Context(), rootEmail, "", otherPassword); !errors.Is(err, boom) {
		t.Fatalf("want the storage error, got %v", err)
	}

	if f.stored(t, root.ID()).Status() != identity.StatusDisabled {
		t.Error("the identity must still be disabled")
	}

	if ok, _, _ := f.hasher.Verify(testPassword, f.storedPassword(t, root).Hash()); !ok {
		t.Error("the previous password must still be the stored one")
	}

	if kept, _ := f.store.sessions.Find(t.Context(), f.master.ID(), live); kept.Ended() {
		t.Error("the session must still be live")
	}
}

func TestResetAdministratorReturnsTheDomainErrorForInvalidInput(t *testing.T) {
	f := newAdminFixture(t)
	root := f.administrator(t, rootEmail, rootUsername)

	_, err := f.admins.ResetAdministrator(t.Context(), "not an email", "", testPassword)
	if !errors.Is(err, identity.ErrEmailInvalid) {
		t.Errorf("email: want ErrEmailInvalid, got %v", err)
	}

	_, err = f.admins.ResetAdministrator(t.Context(), rootEmail, "", "")
	if !errors.Is(err, credential.ErrPasswordInvalid) {
		t.Errorf("password: want ErrPasswordInvalid, got %v", err)
	}

	if f.storedPassword(t, root).MustChange() {
		t.Error("a refused reset must change nothing")
	}
}

func TestHasAdministratorWantsOneAbleToSignIn(t *testing.T) {
	f := newAdminFixture(t)

	has := func() bool {
		t.Helper()

		exists, err := f.admins.HasAdministrator(t.Context())
		if err != nil {
			t.Fatalf("want no error, got %v", err)
		}

		return exists
	}

	if has() {
		t.Error("an empty installation has no administrator")
	}

	// Holds the grant and has no password: nobody can sign in as it.
	f.grantGlobal(t, f.create(t, f.master.ID(), "nopassword@example.com", ""))

	if has() {
		t.Error("a grant holder without a password is not an administrator able to sign in")
	}

	complete := f.administrator(t, "ana@example.com", "ana")

	if !has() {
		t.Error("want the complete administrator counted")
	}

	f.disable(t, complete)

	if has() {
		t.Error("a disabled administrator cannot sign in")
	}
}

func TestTemporaryAdministrators(t *testing.T) {
	f := newAdminFixture(t)

	outcome, err := f.admins.Bootstrap(t.Context(), rootEmail, rootUsername, testPassword)
	if err != nil {
		t.Fatalf("bootstrapping: %v", err)
	}

	root := outcome.Administrator

	// Two grants, to be listed once.
	if _, err := f.admins.GrantRealm(t.Context(), root.ID(), f.acme.ID()); err != nil {
		t.Fatalf("granting acme: %v", err)
	}

	f.administrator(t, "settled@example.com", "")
	f.grantGlobal(t, f.create(t, f.master.ID(), "passwordless@example.com", ""))
	f.createWithPassword(t, f.master.ID(), "ungranted@example.com", "", true)

	temporary, err := f.admins.TemporaryAdministrators(t.Context())
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(temporary) != 1 || temporary[0].ID() != root.ID() {
		t.Fatalf("want only %s, got %d identities", rootEmail, len(temporary))
	}

	if err := f.credentials.SetPassword(t.Context(), f.master.ID(), root.ID(), otherPassword, false); err != nil {
		t.Fatalf("replacing the password: %v", err)
	}

	temporary, err = f.admins.TemporaryAdministrators(t.Context())
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(temporary) != 0 {
		t.Errorf("want none once the password is replaced, got %d", len(temporary))
	}
}

// fakeAdminGrants mimics the two unique indexes and the realm scoping. Grants
// have no setters, so it stores the pointers it is given.
type fakeAdminGrants struct {
	rows map[uuid.UUID]*admin.Grant

	// What HasGlobalAdministrator joins against.
	identities  *fakeIdentities
	credentials *fakeCredentials

	// Fails the next insert without touching the map.
	failNextCreate error

	// Answers the next HasGlobalAdministrator with false whatever is stored:
	// the read of a replica that looked before another one committed.
	hideFirstCheck bool
}

func newFakeAdminGrants(identities *fakeIdentities, credentials *fakeCredentials) *fakeAdminGrants {
	return &fakeAdminGrants{rows: map[uuid.UUID]*admin.Grant{}, identities: identities, credentials: credentials}
}

func (f *fakeAdminGrants) snapshot() func() {
	if f == nil {
		return func() {}
	}

	saved := maps.Clone(f.rows)

	return func() { f.rows = saved }
}

func (f *fakeAdminGrants) Create(_ context.Context, g *admin.Grant) error {
	if err := f.failNextCreate; err != nil {
		f.failNextCreate = nil

		return err
	}

	// One comparison stands for both indexes: a global grant is a nil target.
	for _, existing := range f.rows {
		if existing.IdentityID() == g.IdentityID() && existing.TargetRealmID() == g.TargetRealmID() {
			return admin.ErrExists
		}
	}

	f.rows[g.ID()] = g

	return nil
}

func (f *fakeAdminGrants) ListByIdentity(_ context.Context, realmID, identityID uuid.UUID) ([]*admin.Grant, error) {
	return f.list(func(g *admin.Grant) bool { return g.RealmID() == realmID && g.IdentityID() == identityID }), nil
}

func (f *fakeAdminGrants) ListByRealm(_ context.Context, realmID uuid.UUID, limit int) ([]*admin.Grant, error) {
	if limit <= 0 {
		return nil, service.ErrLimitNotPositive
	}

	listed := f.list(func(g *admin.Grant) bool { return g.RealmID() == realmID })
	if len(listed) > limit {
		listed = listed[:limit]
	}

	return listed, nil
}

// The id is a UUIDv7, so ascending by id is oldest first.
func (f *fakeAdminGrants) list(match func(*admin.Grant) bool) []*admin.Grant {
	var listed []*admin.Grant

	for _, g := range f.rows {
		if match(g) {
			listed = append(listed, g)
		}
	}

	slices.SortFunc(listed, func(a, b *admin.Grant) int {
		left, right := a.ID(), b.ID()

		return bytes.Compare(left[:], right[:])
	})

	return listed
}

func (f *fakeAdminGrants) Delete(_ context.Context, realmID, id uuid.UUID) error {
	stored, ok := f.rows[id]
	if !ok || stored.RealmID() != realmID {
		return admin.ErrNotFound
	}

	delete(f.rows, id)

	return nil
}

func (f *fakeAdminGrants) HasGlobalAdministrator(ctx context.Context, realmID uuid.UUID) (bool, error) {
	if f.hideFirstCheck {
		f.hideFirstCheck = false

		return false, nil
	}

	for _, g := range f.rows {
		if g.RealmID() != realmID || !g.Global() {
			continue
		}

		holder, err := f.identities.FindByID(ctx, realmID, g.IdentityID())
		if err != nil || holder.Status() != identity.StatusActive {
			continue
		}

		if _, err := f.credentials.Find(ctx, realmID, g.IdentityID(), credential.TypePassword); err == nil {
			return true, nil
		}
	}

	return false, nil
}
