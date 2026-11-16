package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
)

func (f *identityFixture) storedPassword(t *testing.T, of *identity.Identity) *credential.Credential {
	t.Helper()

	stored, err := f.store.credentials.Find(t.Context(), of.RealmID(), of.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading the credential of %s: %v", of.Email(), err)
	}

	return stored
}

func TestSetPasswordCreatesTheCredential(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.create(t, realmID, "ana@example.com", "ana")

	if err := f.credentials.SetPassword(t.Context(), realmID, ana.ID(), testPassword, true); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	stored := f.storedPassword(t, ana)

	if ok, _, _ := f.hasher.Verify(testPassword, stored.Hash()); !ok {
		t.Error("the stored hash must be the one of the given password")
	}

	if !stored.MustChange() {
		t.Error("must-change must be stored as given")
	}

	if stored.LastUsedAt() != nil {
		t.Error("setting a password is not using it")
	}
}

func TestSetPasswordReplacesTheOneThereIs(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.createWithPassword(t, realmID, "ana@example.com", "ana", true)
	before := f.storedPassword(t, ana)

	if err := f.credentials.SetPassword(t.Context(), realmID, ana.ID(), "another password", false); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(f.store.credentials.rows) != 1 {
		t.Fatalf("want the row replaced in place, got %d rows", len(f.store.credentials.rows))
	}

	after := f.storedPassword(t, ana)

	if after.ID() != before.ID() {
		t.Error("want the same credential row")
	}

	if ok, _, _ := f.hasher.Verify("another password", after.Hash()); !ok {
		t.Error("the new password must be the stored one")
	}

	if ok, _, _ := f.hasher.Verify(testPassword, after.Hash()); ok {
		t.Error("the old password must stop working")
	}

	if after.MustChange() {
		t.Error("a replacement decides must-change anew")
	}
}

func TestSetPasswordRefusesAnIdentityTheRealmDoesNotHave(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.create(t, realmID, "ana@example.com", "ana")

	cases := map[string]struct{ realmID, identityID uuid.UUID }{
		"unknown identity": {realmID, uuid.New()},
		"another realm":    {uuid.New(), ana.ID()},
	}

	for name, tc := range cases {
		err := f.credentials.SetPassword(t.Context(), tc.realmID, tc.identityID, testPassword, false)
		if !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("%s: want identity.ErrNotFound, got %v", name, err)
		}
	}

	if len(f.store.credentials.rows) != 0 {
		t.Errorf("want no credential written, got %d", len(f.store.credentials.rows))
	}
}

func TestSetPasswordHashesBeforeTheTransactionOpens(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.create(t, realmID, "ana@example.com", "ana")

	if err := f.credentials.SetPassword(t.Context(), realmID, ana.ID(), testPassword, false); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if f.hasher.hashCalls != 1 || f.hasher.callsInTx != 0 {
		t.Errorf("want one hash and none in the transaction, got %d and %d", f.hasher.hashCalls, f.hasher.callsInTx)
	}
}

func TestSetPasswordBoundsTheLength(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.create(t, realmID, "ana@example.com", "ana")

	for name, password := range map[string]string{"empty": "", "one byte over": strings.Repeat("a", 1025)} {
		err := f.credentials.SetPassword(t.Context(), realmID, ana.ID(), password, false)
		if !errors.Is(err, credential.ErrPasswordInvalid) {
			t.Errorf("%s: want ErrPasswordInvalid, got %v", name, err)
		}
	}

	if f.hasher.hashCalls != 0 {
		t.Errorf("a refused password must not be hashed, got %d calls", f.hasher.hashCalls)
	}

	atTheLimit := strings.Repeat("a", 1024)

	if err := f.credentials.SetPassword(t.Context(), realmID, ana.ID(), atTheLimit, false); err != nil {
		t.Fatalf("1024 bytes must be accepted, got %v", err)
	}

	if _, _, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", atTheLimit); err != nil {
		t.Errorf("1024 bytes must verify, got %v", err)
	}
}

func TestSetPasswordReportsAHasherFailure(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.create(t, realmID, "ana@example.com", "ana")

	f.hasher.failHash = errors.New("boom")

	err := f.credentials.SetPassword(t.Context(), realmID, ana.ID(), testPassword, false)
	if !errors.Is(err, f.hasher.failHash) {
		t.Errorf("want the hasher error, got %v", err)
	}

	if len(f.store.credentials.rows) != 0 {
		t.Error("want no credential written")
	}
}

// A caller that could tell these apart could enumerate logins. Each must also
// cost one verification, or the response time tells what the error hides.
func TestVerifyPasswordAnswersEveryFailureWithTheSameMismatch(t *testing.T) {
	realmID, otherRealmID := uuid.New(), uuid.New()

	cases := []struct {
		name     string
		realmID  uuid.UUID
		login    string
		password string

		// False only when a stored hash was there to compare against.
		dummy bool
	}{
		{"unknown email", realmID, "nobody@example.com", testPassword, true},
		{"unknown username", realmID, "nobody", testPassword, true},
		{"malformed email", realmID, "ana@@example.com", testPassword, true},
		{"malformed username", realmID, "not a username", testPassword, true},
		{"empty login", realmID, "", testPassword, true},
		{"login of another realm", otherRealmID, "ana@example.com", testPassword, true},
		{"identity without a password", realmID, "bia@example.com", testPassword, true},
		{"password over the limit", realmID, "ana@example.com", strings.Repeat("a", 1025), true},
		{"wrong password", realmID, "ana@example.com", "not the password", false},
		{"empty password", realmID, "ana@example.com", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIdentityFixture()

			ana := f.createWithPassword(t, realmID, "ana@example.com", "ana", false)
			f.create(t, realmID, "bia@example.com", "")
			f.create(t, otherRealmID, "carla@example.com", "carla")

			storedHash := f.storedPassword(t, ana).Hash()

			found, mustChange, err := f.credentials.VerifyPassword(t.Context(), tc.realmID, tc.login, tc.password)

			// Identity, not errors.Is: a wrapped mismatch carrying a reason
			// would be the leak.
			if err != credential.ErrMismatch {
				t.Fatalf("want exactly ErrMismatch, got %v", err)
			}

			if found != nil || mustChange {
				t.Error("a mismatch must return nothing")
			}

			if f.hasher.verifyCalls != 1 {
				t.Errorf("want exactly one verification, got %d", f.hasher.verifyCalls)
			}

			if usedDummy := f.hasher.lastEncoded != storedHash; usedDummy != tc.dummy {
				t.Errorf("verified against the dummy: want %t, got %t", tc.dummy, usedDummy)
			}

			if len(f.hasher.lastPassword) > 1024 {
				t.Errorf("a password over the limit must not reach the hasher, got %d bytes", len(f.hasher.lastPassword))
			}

			if f.store.credentials.updates != 0 {
				t.Error("a mismatch must not write")
			}
		})
	}
}

func TestVerifyPasswordBuildsTheDummyOnce(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	for _, login := range []string{"nobody", "nobody@example.com", "somebody"} {
		_, _, err := f.credentials.VerifyPassword(t.Context(), realmID, login, testPassword)
		if !errors.Is(err, credential.ErrMismatch) {
			t.Fatalf("want ErrMismatch, got %v", err)
		}
	}

	if f.hasher.hashCalls != 1 {
		t.Errorf("want the dummy hashed once, got %d", f.hasher.hashCalls)
	}

	if f.hasher.verifyCalls != 3 {
		t.Errorf("want one verification per attempt, got %d", f.hasher.verifyCalls)
	}
}

func TestVerifyPasswordReportsADummyThatCannotBeBuilt(t *testing.T) {
	f := newIdentityFixture()

	f.hasher.failHash = errors.New("boom")

	_, _, err := f.credentials.VerifyPassword(t.Context(), uuid.New(), "nobody", testPassword)
	if !errors.Is(err, f.hasher.failHash) {
		t.Errorf("want the hasher error, got %v", err)
	}
}

// A stored hash that cannot be evaluated is a broken row, not a wrong password.
func TestVerifyPasswordReportsAHasherErrorOnAStoredHash(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	f.createWithPassword(t, realmID, "ana@example.com", "ana", false)
	f.hasher.failVerify = errors.New("boom")

	found, _, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", testPassword)
	if !errors.Is(err, f.hasher.failVerify) {
		t.Errorf("want the hasher error, got %v", err)
	}

	if errors.Is(err, credential.ErrMismatch) {
		t.Error("an unreadable hash must not be reported as a mismatch")
	}

	if found != nil {
		t.Error("want no identity")
	}
}

func TestVerifyPasswordAcceptsEmailAndUsernameInAnyCase(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.createWithPassword(t, realmID, "ana@example.com", "ana.silva", false)

	for _, login := range []string{"ana@example.com", " ANA@Example.com", "ana.silva", "Ana.SILVA "} {
		found, mustChange, err := f.credentials.VerifyPassword(t.Context(), realmID, login, testPassword)
		if err != nil {
			t.Errorf("login %q: want no error, got %v", login, err)

			continue
		}

		if found.ID() != ana.ID() {
			t.Errorf("login %q: want %s, got %s", login, ana.ID(), found.ID())
		}

		if mustChange {
			t.Errorf("login %q: must-change was not set", login)
		}
	}
}

func TestVerifyPasswordReportsMustChange(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()

	f.createWithPassword(t, realmID, "ana@example.com", "ana", true)

	_, mustChange, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", testPassword)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if !mustChange {
		t.Error("want must-change reported")
	}
}

func TestVerifyPasswordRecordsTheUse(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.createWithPassword(t, realmID, "ana@example.com", "ana", false)
	before := f.storedPassword(t, ana).Hash()

	if _, _, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", testPassword); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	stored := f.storedPassword(t, ana)

	if stored.LastUsedAt() == nil {
		t.Error("the use must be persisted")
	}

	if f.store.credentials.updates != 1 {
		t.Errorf("want one write, got %d", f.store.credentials.updates)
	}

	if stored.Hash() != before || f.hasher.hashCalls != 1 {
		t.Error("a hash that needs no rehash must be left alone")
	}
}

// The disabled check comes after the password: someone who does not hold it
// learns nothing about the account's state.
func TestVerifyPasswordRefusesADisabledIdentityOnlyOnceThePasswordIsProven(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.createWithPassword(t, realmID, "ana@example.com", "ana", false)

	if err := f.identities.SetStatus(t.Context(), realmID, ana.ID(), identity.StatusDisabled); err != nil {
		t.Fatalf("disabling: %v", err)
	}

	found, _, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", testPassword)
	if !errors.Is(err, identity.ErrDisabled) {
		t.Errorf("right password: want ErrDisabled, got %v", err)
	}

	if found != nil {
		t.Error("a disabled identity must not be returned")
	}

	_, _, err = f.credentials.VerifyPassword(t.Context(), realmID, "ana", "not the password")
	if err != credential.ErrMismatch {
		t.Errorf("wrong password: want exactly ErrMismatch, got %v", err)
	}

	if f.storedPassword(t, ana).LastUsedAt() != nil || f.store.credentials.updates != 0 {
		t.Error("a refused login is not a use")
	}
}

func TestVerifyPasswordRehashesWhenTheHasherAsks(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.createWithPassword(t, realmID, "ana@example.com", "ana", true)
	before := f.storedPassword(t, ana).Hash()

	f.hasher.generation++
	f.hasher.needsRehash = true

	_, mustChange, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", testPassword)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	stored := f.storedPassword(t, ana)

	if stored.Hash() == before || !strings.HasPrefix(stored.Hash(), "$fake$1$") {
		t.Errorf("want the hash rewritten under the new parameters, got %q", stored.Hash())
	}

	if ok, _, _ := f.hasher.Verify(testPassword, stored.Hash()); !ok {
		t.Error("the rehash must be of the same password")
	}

	// A rehash is not a new secret: whoever was told to change it still is.
	if !mustChange || !stored.MustChange() {
		t.Error("a rehash must keep must-change")
	}

	if stored.LastUsedAt() == nil {
		t.Error("the use must be persisted along with the rehash")
	}

	if f.store.credentials.updates != 1 {
		t.Errorf("want the use and the rehash in one write, got %d", f.store.credentials.updates)
	}

	if f.hasher.callsInTx != 0 {
		t.Errorf("want no hashing inside a transaction, got %d calls", f.hasher.callsInTx)
	}
}

// A password replaced between the read and the write must survive: the write
// carries the hash that was read, and would put the old password back.
func TestVerifyPasswordDoesNotUndoAPasswordSetMeanwhile(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.createWithPassword(t, realmID, "ana@example.com", "ana", false)

	f.hasher.needsRehash = true

	f.store.credentials.beforeConditional = func() {
		if err := f.credentials.SetPassword(t.Context(), realmID, ana.ID(), "the new password", true); err != nil {
			t.Fatalf("setting a password meanwhile: %v", err)
		}
	}

	if _, _, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", testPassword); err != nil {
		t.Fatalf("the password was right when it was checked, got %v", err)
	}

	stored := f.storedPassword(t, ana)

	if ok, _, _ := f.hasher.Verify("the new password", stored.Hash()); !ok {
		t.Error("the password set meanwhile was overwritten")
	}

	if !stored.MustChange() || stored.LastUsedAt() != nil {
		t.Error("nothing of the stale write may land")
	}
}

func TestVerifyPasswordWritesNothingWhenTheRehashFails(t *testing.T) {
	f := newIdentityFixture()
	realmID := uuid.New()
	ana := f.createWithPassword(t, realmID, "ana@example.com", "ana", false)

	f.hasher.needsRehash = true
	f.hasher.failHash = errors.New("boom")

	_, _, err := f.credentials.VerifyPassword(t.Context(), realmID, "ana", testPassword)
	if !errors.Is(err, f.hasher.failHash) {
		t.Errorf("want the hasher error, got %v", err)
	}

	if f.store.credentials.updates != 0 || f.storedPassword(t, ana).LastUsedAt() != nil {
		t.Error("want nothing written")
	}
}

// fakeCredentials mimics the unique index on identity and type, and the realm
// scoping. Like fakeIdentities it stores and returns copies.
type fakeCredentials struct {
	rows map[uuid.UUID]*credential.Credential

	updates int

	// Fails the next insert without touching the map.
	failNextCreate error

	// Runs once, between a verification's read and its write.
	beforeConditional func()
}

func newFakeCredentials() *fakeCredentials {
	return &fakeCredentials{rows: map[uuid.UUID]*credential.Credential{}}
}

func (f *fakeCredentials) snapshot() func() {
	if f == nil {
		return func() {}
	}

	saved := maps.Clone(f.rows)

	return func() { f.rows = saved }
}

func (f *fakeCredentials) Create(_ context.Context, c *credential.Credential) error {
	if err := f.failNextCreate; err != nil {
		f.failNextCreate = nil

		return err
	}

	for _, existing := range f.rows {
		if existing.IdentityID() == c.IdentityID() && existing.Type() == c.Type() {
			return credential.ErrExists
		}
	}

	f.rows[c.ID()] = copyCredential(c)

	return nil
}

func (f *fakeCredentials) Find(
	_ context.Context, realmID, identityID uuid.UUID, typ credential.Type,
) (*credential.Credential, error) {
	for _, candidate := range f.rows {
		if candidate.RealmID() == realmID && candidate.IdentityID() == identityID && candidate.Type() == typ {
			return copyCredential(candidate), nil
		}
	}

	return nil, credential.ErrNotFound
}

func (f *fakeCredentials) Update(_ context.Context, c *credential.Credential) error {
	stored, ok := f.rows[c.ID()]
	if !ok || stored.RealmID() != c.RealmID() {
		return credential.ErrNotFound
	}

	f.rows[c.ID()] = copyCredential(c)
	f.updates++

	return nil
}

func (f *fakeCredentials) UpdateIfHash(
	_ context.Context, c *credential.Credential, previousHash string,
) (bool, error) {
	if hook := f.beforeConditional; hook != nil {
		f.beforeConditional = nil

		hook()
	}

	stored, ok := f.rows[c.ID()]
	if !ok || stored.RealmID() != c.RealmID() || stored.Hash() != previousHash {
		return false, nil
	}

	f.rows[c.ID()] = copyCredential(c)
	f.updates++

	return true, nil
}

func copyCredential(c *credential.Credential) *credential.Credential {
	copied, err := credential.Rehydrate(
		c.ID(), c.RealmID(), c.IdentityID(), c.Type(), c.Hash(), c.MustChange(),
		c.LastUsedAt(), c.CreatedAt(), c.UpdatedAt(),
	)
	if err != nil {
		panic(err)
	}

	return copied
}

// fakeHasher is a bare digest behind a PHC-looking prefix. The generation stands
// in for the cost parameters: bumping it makes new hashes differ from old ones
// while the old ones keep verifying.
type fakeHasher struct {
	tx *txProbe

	generation  int
	needsRehash bool
	failHash    error
	failVerify  error

	hashCalls   int
	verifyCalls int
	callsInTx   int

	lastPassword string
	lastEncoded  string
}

func (f *fakeHasher) Hash(password string) (string, error) {
	f.hashCalls++
	f.noteTx()

	if f.failHash != nil {
		return "", f.failHash
	}

	return fmt.Sprintf("$fake$%d$%s", f.generation, digest(password)), nil
}

func (f *fakeHasher) Verify(password, encoded string) (bool, bool, error) {
	f.verifyCalls++
	f.noteTx()
	f.lastPassword = password
	f.lastEncoded = encoded

	if f.failVerify != nil {
		return false, false, f.failVerify
	}

	if !strings.HasSuffix(encoded, "$"+digest(password)) {
		return false, false, nil
	}

	return true, f.needsRehash, nil
}

func (f *fakeHasher) noteTx() {
	if f.tx.open {
		f.callsInTx++
	}
}

func digest(password string) string {
	sum := sha256.Sum256([]byte(password))

	return hex.EncodeToString(sum[:])
}
