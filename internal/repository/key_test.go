package repository_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/service"
)

func TestKeyCreateAndFindActive(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	created := makeKey(t, realmID)

	if err := store.Keys().Create(context.Background(), created); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	found, err := store.Keys().FindActive(
		context.Background(), realmID, key.PurposeSignature, key.AlgorithmES256,
	)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if found.KID() != created.KID() {
		t.Errorf("want kid %q, got %q", created.KID(), found.KID())
	}

	if found.KEKID() != created.KEKID() {
		t.Errorf("want kek id %q, got %q", created.KEKID(), found.KEKID())
	}

	if string(found.Sealed()) != string(created.Sealed()) {
		t.Error("want the sealed material to round trip, got different bytes")
	}

	original, err := created.PublicKeyDER()
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	back, err := found.PublicKeyDER()
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if string(original) != string(back) {
		t.Error("want the public key to round trip, got different der")
	}
}

func TestKeyFindActiveReportsAbsence(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	_, err := store.Keys().FindActive(
		context.Background(), realmID, key.PurposeSignature, key.AlgorithmRS256,
	)
	if !errors.Is(err, key.ErrNoActiveKey) {
		t.Fatalf("want ErrNoActiveKey, got %v", err)
	}
}

func TestKeyFindByKIDReportsAbsence(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	_, err := store.Keys().FindByKID(context.Background(), realmID, "nope")
	if !errors.Is(err, key.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestKeyRefusesASecondActiveKey(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	if err := store.Keys().Create(context.Background(), makeKey(t, realmID)); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	err := store.Keys().Create(context.Background(), makeKey(t, realmID))
	if !errors.Is(err, key.ErrActiveKeyExists) {
		t.Fatalf("want ErrActiveKeyExists, got %v", err)
	}
}

func TestKeyAllowsManyPassiveKeys(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	for range 3 {
		created := makeKey(t, realmID)

		if err := store.Keys().Create(context.Background(), created); err != nil {
			t.Fatalf("creating: %v", err)
		}

		if err := created.Deactivate(); err != nil {
			t.Fatalf("deactivating: %v", err)
		}

		if err := store.Keys().Update(context.Background(), created); err != nil {
			t.Fatalf("updating: %v", err)
		}
	}

	found, err := store.Keys().ListByRealm(
		context.Background(), realmID, []key.Status{key.StatusPassive},
	)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(found) != 3 {
		t.Errorf("want 3 passive keys, got %d", len(found))
	}
}

func TestKeyAllowsTheSameSlotInAnotherRealm(t *testing.T) {
	store := newStore(t)
	first := seedRealm(t, store, "acme")
	second := seedRealm(t, store, "globex")

	if err := store.Keys().Create(context.Background(), makeKey(t, first)); err != nil {
		t.Fatalf("first realm: %v", err)
	}

	if err := store.Keys().Create(context.Background(), makeKey(t, second)); err != nil {
		t.Fatalf("want no error, got %v", err)
	}
}

func TestKeyUpdateWritesOnlyTheStatusColumns(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	created := makeKey(t, realmID)
	if err := store.Keys().Create(context.Background(), created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := created.Deactivate(); err != nil {
		t.Fatalf("deactivating: %v", err)
	}

	if err := store.Keys().Update(context.Background(), created); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	found, err := store.Keys().FindByKID(context.Background(), realmID, created.KID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if found.Status() != key.StatusPassive {
		t.Errorf("want passive, got %q", found.Status())
	}

	// The freed slot is the only way to observe that the marker followed the status.
	if err := store.Keys().Create(context.Background(), makeKey(t, realmID)); err != nil {
		t.Errorf("want the slot free after deactivation, got %v", err)
	}
}

func TestKeyListByRealmOrdersNewestFirst(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	first := makeKey(t, realmID)
	if err := store.Keys().Create(context.Background(), first); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := first.Deactivate(); err != nil {
		t.Fatalf("deactivating: %v", err)
	}

	if err := store.Keys().Update(context.Background(), first); err != nil {
		t.Fatalf("updating: %v", err)
	}

	second := makeKey(t, realmID)
	if err := store.Keys().Create(context.Background(), second); err != nil {
		t.Fatalf("creating: %v", err)
	}

	found, err := store.Keys().ListByRealm(context.Background(), realmID, nil)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(found) != 2 {
		t.Fatalf("want 2 keys, got %d", len(found))
	}

	if found[0].KID() != second.KID() {
		t.Errorf("want the newest key first, got %q", found[0].KID())
	}
}

func TestKeyRewrapWritesTheMaterialAndTheKEK(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	created := makeKey(t, realmID)
	if err := store.Keys().Create(context.Background(), created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := created.Rewrapped([]byte("resealed"), "kek-next"); err != nil {
		t.Fatalf("rewrapping: %v", err)
	}

	if err := store.Keys().Rewrap(context.Background(), created); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	found, err := store.Keys().FindByKID(context.Background(), realmID, created.KID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if string(found.Sealed()) != "resealed" || found.KEKID() != "kek-next" {
		t.Errorf("want the resealed material, got %q under %q", found.Sealed(), found.KEKID())
	}

	if found.Status() != key.StatusActive {
		t.Errorf("want the status untouched, got %q", found.Status())
	}
}

func TestKeyListStaleKEK(t *testing.T) {
	store := newStore(t)
	realmID := seedRealm(t, store, "acme")

	created := makeKey(t, realmID)
	if err := store.Keys().Create(context.Background(), created); err != nil {
		t.Fatalf("creating: %v", err)
	}

	stale, err := store.Keys().ListStaleKEK(context.Background(), "kek-next", 10)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(stale) != 1 {
		t.Fatalf("want 1 stale row, got %d", len(stale))
	}

	current, err := store.Keys().ListStaleKEK(context.Background(), created.KEKID(), 10)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(current) != 0 {
		t.Errorf("want nothing stale under its own kek, got %d", len(current))
	}
}

func seedRealm(t *testing.T, store service.Store, slug string) uuid.UUID {
	t.Helper()

	created := makeRealm(t, slug)

	if err := store.Realms().Create(context.Background(), created); err != nil {
		t.Fatalf("creating a realm: %v", err)
	}

	return created.ID()
}

func makeKey(t *testing.T, realmID uuid.UUID) *key.Key {
	t.Helper()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	// Not really sealed: this layer stores bytes, sealing is the service's business.
	pkcs8, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	built, err := key.New(
		realmID, key.PurposeSignature, key.AlgorithmES256, &private.PublicKey, pkcs8, "kek-current",
	)
	if err != nil {
		t.Fatalf("building a key: %v", err)
	}

	return built
}
