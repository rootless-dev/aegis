package repository_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/service"
)

// Shaped like a PHC string; this layer stores it and never evaluates it.
const (
	firstHash  = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$Zmlyc3RmaXJzdGZpcnN0"
	secondHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$c2Vjb25kc2Vjb25kc2Vj"
)

func TestCredentialRoundTripWithoutAUse(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	want := makeCredential(t, owner, true)

	if err := store.Credentials().Create(ctx, want); err != nil {
		t.Fatalf("creating: %v", err)
	}

	got, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.ID() != want.ID() || got.RealmID() != realmID || got.IdentityID() != owner.ID() {
		t.Errorf("got %s in realm %s for identity %s", got.ID(), got.RealmID(), got.IdentityID())
	}

	if got.Type() != credential.TypePassword || got.Hash() != firstHash {
		t.Errorf("got type %q and hash %q", got.Type(), got.Hash())
	}

	if !got.MustChange() {
		t.Error("must_change: want true")
	}

	if got.LastUsedAt() != nil {
		t.Errorf("last_used_at: want nil, got %s", got.LastUsedAt())
	}

	if stored := rawRow(t, db, "credentials", want.ID())["last_used_at"]; stored != nil {
		t.Errorf("want NULL in the column, got %#v", stored)
	}

	if !got.CreatedAt().Equal(want.CreatedAt()) || !got.UpdatedAt().Equal(want.UpdatedAt()) {
		t.Errorf("timestamps: got %s and %s", got.CreatedAt(), got.UpdatedAt())
	}
}

func TestCredentialRoundTripWithAUse(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	lastUsedAt := time.Date(2026, 3, 4, 5, 6, 9, 987654000, time.UTC)
	createdAt := time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)

	want, err := credential.Rehydrate(
		id, realmID, owner.ID(), credential.TypePassword, firstHash, false, &lastUsedAt, createdAt, lastUsedAt,
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Credentials().Create(ctx, want); err != nil {
		t.Fatalf("creating: %v", err)
	}

	got, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.MustChange() {
		t.Error("must_change: want false")
	}

	if got.LastUsedAt() == nil || !got.LastUsedAt().Equal(lastUsedAt) {
		t.Errorf("last_used_at: want %s, got %v", lastUsedAt, got.LastUsedAt())
	}

	if !got.CreatedAt().Equal(createdAt) {
		t.Errorf("created_at: want %s, got %s", createdAt, got.CreatedAt())
	}
}

func TestCredentialRefusesASecondOfTheSameType(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	if err := store.Credentials().Create(ctx, makeCredential(t, owner, false)); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	err := store.Credentials().Create(ctx, makeCredential(t, owner, false))
	if !errors.Is(err, credential.ErrExists) {
		t.Fatalf("want credential.ErrExists, got %v", err)
	}
}

func TestCredentialFindIsScopedToTheRealm(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	if err := store.Credentials().Create(ctx, makeCredential(t, owner, false)); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, err := store.Credentials().Find(ctx, other, owner.ID(), credential.TypePassword)
	if !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("want credential.ErrNotFound, got %v", err)
	}
}

func TestCredentialFindReportsAbsence(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	_, err := store.Credentials().Find(context.Background(), realmID, owner.ID(), credential.TypePassword)
	if !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("want credential.ErrNotFound, got %v", err)
	}
}

func TestCredentialUpdateWritesTheSecretAndItsUse(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	created := makeCredential(t, owner, true)
	if err := store.Credentials().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := created.Replace(secondHash, false); err != nil {
		t.Fatalf("replacing: %v", err)
	}

	created.MarkUsed()

	if err := store.Credentials().Update(ctx, created); err != nil {
		t.Fatalf("updating: %v", err)
	}

	got, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Hash() != secondHash {
		t.Errorf("hash: got %q", got.Hash())
	}

	// false is a zero value, the one a struct-based Updates would have skipped.
	if got.MustChange() {
		t.Error("must_change: want false written over true")
	}

	if got.LastUsedAt() == nil || !got.LastUsedAt().Equal(*created.LastUsedAt()) {
		t.Errorf("last_used_at: want %s, got %v", created.LastUsedAt(), got.LastUsedAt())
	}

	if !got.UpdatedAt().Equal(created.UpdatedAt()) {
		t.Errorf("updated_at: want %s, got %s", created.UpdatedAt(), got.UpdatedAt())
	}
}

func TestCredentialUpdateIfHashAppliesWhileTheHashIsUnchanged(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	created := makeCredential(t, owner, true)
	if err := store.Credentials().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	created.MarkUsed()

	applied, err := store.Credentials().UpdateIfHash(ctx, created, firstHash)
	if err != nil || !applied {
		t.Fatalf("want the write applied, got %v, %v", applied, err)
	}

	got, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.LastUsedAt() == nil || !got.LastUsedAt().Equal(*created.LastUsedAt()) {
		t.Errorf("last_used_at: want %s, got %v", created.LastUsedAt(), got.LastUsedAt())
	}
}

func TestCredentialUpdateIfHashLeavesAReplacedHashAlone(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	created := makeCredential(t, owner, false)
	if err := store.Credentials().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	stale, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	if err := created.Replace(secondHash, true); err != nil {
		t.Fatalf("replacing: %v", err)
	}

	if err := store.Credentials().Update(ctx, created); err != nil {
		t.Fatalf("updating: %v", err)
	}

	stale.MarkUsed()

	applied, err := store.Credentials().UpdateIfHash(ctx, stale, firstHash)
	if err != nil || applied {
		t.Fatalf("want the stale write refused without an error, got %v, %v", applied, err)
	}

	got, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Hash() != secondHash || !got.MustChange() || got.LastUsedAt() != nil {
		t.Errorf("the stale write landed: hash %q, must_change %v, last_used_at %v",
			got.Hash(), got.MustChange(), got.LastUsedAt())
	}
}

func TestCredentialUpdateIfHashCannotCrossRealms(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	otherRealm := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	created := makeCredential(t, owner, false)
	if err := store.Credentials().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	foreign, err := credential.Rehydrate(
		created.ID(), otherRealm, created.IdentityID(), created.Type(), secondHash, true,
		nil, created.CreatedAt(), created.UpdatedAt(),
	)
	if err != nil {
		t.Fatalf("building the foreign copy: %v", err)
	}

	applied, err := store.Credentials().UpdateIfHash(ctx, foreign, firstHash)
	if err != nil || applied {
		t.Fatalf("want nothing written across realms, got %v, %v", applied, err)
	}

	got, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Hash() != firstHash {
		t.Errorf("the write crossed realms: hash %q", got.Hash())
	}
}

// The aggregate never clears last_used_at, so the cleared copy comes through
// Rehydrate: NULL is the other zero value Update has to write.
func TestCredentialUpdateWritesANullLastUse(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	created := makeCredential(t, owner, false)
	created.MarkUsed()

	if err := store.Credentials().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	cleared, err := credential.Rehydrate(
		created.ID(), realmID, owner.ID(), credential.TypePassword, firstHash, false, nil,
		created.CreatedAt(), created.UpdatedAt().Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Credentials().Update(ctx, cleared); err != nil {
		t.Fatalf("updating: %v", err)
	}

	if stored := rawRow(t, db, "credentials", created.ID())["last_used_at"]; stored != nil {
		t.Errorf("want NULL in the column, got %#v", stored)
	}
}

func TestCredentialUpdateWritesOnlyItsFourColumns(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")
	// A real row, so that a write to identity_id would pass the foreign key
	// and show up below instead of failing for another reason.
	stranger := seedIdentity(t, store, realmID, "grace@example.com", "")

	created := makeCredential(t, owner, false)
	if err := store.Credentials().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	before := rawRow(t, db, "credentials", created.ID())

	diverging, err := credential.Rehydrate(
		created.ID(), realmID, stranger.ID(), credential.TypePassword, secondHash, true, nil,
		created.CreatedAt().Add(-time.Hour), created.UpdatedAt().Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Credentials().Update(ctx, diverging); err != nil {
		t.Fatalf("updating: %v", err)
	}

	after := rawRow(t, db, "credentials", created.ID())

	for _, column := range []string{"id", "realm_id", "identity_id", "type", "created_at"} {
		if fmt.Sprint(before[column]) != fmt.Sprint(after[column]) {
			t.Errorf("%s must be untouched by Update: %v became %v", column, before[column], after[column])
		}
	}

	if after["hash"] != secondHash {
		t.Errorf("hash: got %v", after["hash"])
	}
}

func TestCredentialUpdateCannotCrossRealms(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	home := seedRealm(t, store, "acme")
	other := seedRealm(t, store, "globex")
	owner := seedIdentity(t, store, home, "ada@example.com", "")

	created := makeCredential(t, owner, false)
	if err := store.Credentials().Create(ctx, created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	foreign, err := credential.Rehydrate(
		created.ID(), other, owner.ID(), credential.TypePassword, secondHash, true, nil,
		created.CreatedAt(), created.UpdatedAt().Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := store.Credentials().Update(ctx, foreign); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("want credential.ErrNotFound, got %v", err)
	}

	got, err := store.Credentials().Find(ctx, home, owner.ID(), credential.TypePassword)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.Hash() != firstHash || got.MustChange() {
		t.Error("the row in its own realm must be untouched")
	}
}

func TestCredentialUpdateOfAMissingRowReportsNotFound(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	never := makeCredential(t, owner, false)

	if err := store.Credentials().Update(context.Background(), never); !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("want credential.ErrNotFound, got %v", err)
	}
}

func TestDeletingARealmRemovesItsIdentitiesAndCredentials(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	if err := store.Credentials().Create(ctx, makeCredential(t, owner, false)); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// Raw SQL: no repository method deletes a realm, on purpose.
	if err := db.Exec("DELETE FROM realms WHERE id = ?", realmID.String()).Error; err != nil {
		t.Fatalf("deleting the realm: %v", err)
	}

	if _, err := store.Identities().FindByID(ctx, realmID, owner.ID()); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("want the identity gone with the realm, got %v", err)
	}

	_, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("want the credential gone with the realm, got %v", err)
	}
}

func TestDeletingAnIdentityRemovesItsCredentials(t *testing.T) {
	db, store := newStoreAndDB(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")
	owner := seedIdentity(t, store, realmID, "ada@example.com", "")

	if err := store.Credentials().Create(ctx, makeCredential(t, owner, false)); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if err := db.Exec("DELETE FROM identities WHERE id = ?", owner.ID().String()).Error; err != nil {
		t.Fatalf("deleting the identity: %v", err)
	}

	_, err := store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("want the credential gone with the identity, got %v", err)
	}
}

func TestInTxRollsBackAnIdentityAndItsCredentialTogether(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	realmID := seedRealm(t, store, "acme")

	owner := makeIdentity(t, realmID, "ada@example.com", "")
	sentinel := errors.New("stop")

	err := store.InTx(ctx, func(tx service.Store) error {
		if err := tx.Identities().Create(ctx, owner); err != nil {
			return err
		}

		if err := tx.Credentials().Create(ctx, makeCredential(t, owner, false)); err != nil {
			return err
		}

		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("the callback error must reach the caller, got %v", err)
	}

	if _, err := store.Identities().FindByID(ctx, realmID, owner.ID()); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("the identity survived a rollback: %v", err)
	}

	_, err = store.Credentials().Find(ctx, realmID, owner.ID(), credential.TypePassword)
	if !errors.Is(err, credential.ErrNotFound) {
		t.Errorf("the credential survived a rollback: %v", err)
	}
}

func makeCredential(t *testing.T, owner *identity.Identity, mustChange bool) *credential.Credential {
	t.Helper()

	built, err := credential.NewPassword(owner.RealmID(), owner.ID(), firstHash, mustChange)
	if err != nil {
		t.Fatalf("building a credential: %v", err)
	}

	return built
}
