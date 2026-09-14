package service_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/service"
)

const testKEKID = "kek-current"

func TestPrepareProducesOneKeyPerAlgorithm(t *testing.T) {
	keys, _, _, _ := newKeyService(t)

	realmID := uuid.New()

	prepared, err := keys.Prepare(realmID)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(prepared) != 2 {
		t.Fatalf("want 2 keys, got %d", len(prepared))
	}

	got := []key.Algorithm{prepared[0].Algorithm(), prepared[1].Algorithm()}
	if !slices.Contains(got, key.AlgorithmRS256) || !slices.Contains(got, key.AlgorithmES256) {
		t.Errorf("want RS256 and ES256, got %v", got)
	}

	for _, prep := range prepared {
		if prep.Status() != key.StatusActive {
			t.Errorf("want active, got %q", prep.Status())
		}

		if prep.RealmID() != realmID {
			t.Errorf("want realm %s, got %s", realmID, prep.RealmID())
		}

		if prep.KEKID() != testKEKID {
			t.Errorf("want the sealer's kek id, got %q", prep.KEKID())
		}
	}
}

// The split exists so no transaction is ever held across an RSA generation.
func TestPrepareTouchesNoRepository(t *testing.T) {
	keys, store, _, _ := newKeyService(t)

	if _, err := keys.Prepare(uuid.New()); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(store.keys.byID) != 0 {
		t.Errorf("want nothing persisted, got %d rows", len(store.keys.byID))
	}
}

func TestPrepareSealsWithTheRealmAndKIDAsAAD(t *testing.T) {
	keys, _, sealer, _ := newKeyService(t)

	realmID := uuid.New()

	prepared, err := keys.Prepare(realmID, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	want := []byte(realmID.String() + "|" + prepared[0].KID())
	if !bytes.Equal(sealer.lastAAD, want) {
		t.Errorf("want aad %q, got %q", want, sealer.lastAAD)
	}
}

func TestEnsureKeysCreatesWhatIsMissing(t *testing.T) {
	keys, store, _, _ := newKeyService(t)

	realmID := uuid.New()

	published, err := keys.EnsureKeys(context.Background(), realmID)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(published) != 2 {
		t.Errorf("want 2 keys, got %d", len(published))
	}

	if len(store.keys.byID) != 2 {
		t.Errorf("want 2 rows, got %d", len(store.keys.byID))
	}
}

func TestEnsureKeysIsIdempotent(t *testing.T) {
	keys, store, _, generator := newKeyService(t)

	realmID := uuid.New()

	if _, err := keys.EnsureKeys(context.Background(), realmID); err != nil {
		t.Fatalf("first call: %v", err)
	}

	generated := generator.calls

	if _, err := keys.EnsureKeys(context.Background(), realmID); err != nil {
		t.Fatalf("second call: %v", err)
	}

	if len(store.keys.byID) != 2 {
		t.Errorf("want 2 rows after two calls, got %d", len(store.keys.byID))
	}

	// The read has to come before the generation, or every boot burns two key
	// generations only to discard them.
	if generator.calls != generated {
		t.Errorf("want no further generation, got %d more", generator.calls-generated)
	}
}

func TestEnsureKeysFillsOnlyTheMissingAlgorithm(t *testing.T) {
	keys, _, _, generator := newKeyService(t)

	realmID := uuid.New()

	if _, err := keys.EnsureKeys(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Fatalf("seeding es256: %v", err)
	}

	before := generator.calls

	if _, err := keys.EnsureKeys(context.Background(), realmID); err != nil {
		t.Fatalf("filling the gap: %v", err)
	}

	if generator.calls-before != 1 {
		t.Errorf("want exactly one generation, got %d", generator.calls-before)
	}
}

// Another replica seeded between the read and the insert: the unique index
// resolves it, and the loser adopts the winning row rather than failing the boot.
func TestEnsureKeysToleratesALostRace(t *testing.T) {
	keys, store, _, _ := newKeyService(t)

	realmID := uuid.New()

	seeded, err := keys.Prepare(realmID, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("preparing the winner: %v", err)
	}

	store.keys.onNextCreate = func() error {
		// The winning replica's row appears just before our insert.
		return store.keys.insert(seeded[0])
	}

	published, err := keys.EnsureKeys(context.Background(), realmID, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if len(published) != 1 {
		t.Errorf("want 1 key, got %d", len(published))
	}

	if published[0].KID() != seeded[0].KID() {
		t.Errorf("want the winner's key adopted, got %q", published[0].KID())
	}
}

func TestRotateDeactivatesTheOutgoingKey(t *testing.T) {
	keys, store, _, _ := newKeyService(t)

	realmID := uuid.New()

	if _, err := keys.EnsureKeys(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	outgoing, err := store.Keys().FindActive(context.Background(), realmID, key.PurposeSignature, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("reading the active key: %v", err)
	}

	replacement, err := keys.Rotate(context.Background(), realmID, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if replacement.KID() == outgoing.KID() {
		t.Error("want a new key, got the same kid")
	}

	if store.keys.byID[outgoing.ID()].Status() != key.StatusPassive {
		t.Errorf("want the outgoing key passive, got %q", store.keys.byID[outgoing.ID()].Status())
	}

	if store.keys.byID[replacement.ID()].Status() != key.StatusActive {
		t.Errorf("want the replacement active, got %q", store.keys.byID[replacement.ID()].Status())
	}
}

// Rotation is the recovery path after a mistaken disable, so an absent active
// key is ordinary rather than an error.
func TestRotateWorksWithNoActiveKey(t *testing.T) {
	keys, _, _, _ := newKeyService(t)

	created, err := keys.Rotate(context.Background(), uuid.New(), key.AlgorithmRS256)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if created.Status() != key.StatusActive {
		t.Errorf("want active, got %q", created.Status())
	}
}

func TestDisableRefusesTheActiveKey(t *testing.T) {
	keys, store, _, _ := newKeyService(t)

	realmID := uuid.New()

	if _, err := keys.EnsureKeys(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	active, err := store.Keys().FindActive(context.Background(), realmID, key.PurposeSignature, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("reading the active key: %v", err)
	}

	err = keys.Disable(context.Background(), realmID, active.KID())
	if !errors.Is(err, key.ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestDisableRemovesAPassiveKeyFromPublished(t *testing.T) {
	keys, store, _, _ := newKeyService(t)

	realmID := uuid.New()

	if _, err := keys.EnsureKeys(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	outgoing, err := store.Keys().FindActive(context.Background(), realmID, key.PurposeSignature, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("reading the active key: %v", err)
	}

	if _, err := keys.Rotate(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Fatalf("rotating: %v", err)
	}

	if err := keys.Disable(context.Background(), realmID, outgoing.KID()); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	published, err := keys.Published(context.Background(), realmID)
	if err != nil {
		t.Fatalf("listing published: %v", err)
	}

	if len(published) != 1 {
		t.Errorf("want 1 published key, got %d", len(published))
	}

	all, err := keys.List(context.Background(), realmID)
	if err != nil {
		t.Fatalf("listing all: %v", err)
	}

	// List shows the disabled key: the operator has to see what to enable.
	if len(all) != 2 {
		t.Errorf("want 2 keys in total, got %d", len(all))
	}
}

func TestEnableReturnsADisabledKeyToPublished(t *testing.T) {
	keys, store, _, _ := newKeyService(t)

	realmID := uuid.New()

	if _, err := keys.EnsureKeys(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	outgoing, err := store.Keys().FindActive(context.Background(), realmID, key.PurposeSignature, key.AlgorithmES256)
	if err != nil {
		t.Fatalf("reading the active key: %v", err)
	}

	if _, err := keys.Rotate(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Fatalf("rotating: %v", err)
	}

	if err := keys.Disable(context.Background(), realmID, outgoing.KID()); err != nil {
		t.Fatalf("disabling: %v", err)
	}

	if err := keys.Enable(context.Background(), realmID, outgoing.KID()); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if store.keys.byID[outgoing.ID()].Status() != key.StatusPassive {
		t.Errorf("want passive, got %q", store.keys.byID[outgoing.ID()].Status())
	}
}

func TestSignerForReturnsAUsableSigner(t *testing.T) {
	keys, _, _, _ := newKeyService(t)

	realmID := uuid.New()

	published, err := keys.EnsureKeys(context.Background(), realmID, key.AlgorithmRS256)
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	signer, ref, err := keys.SignerFor(context.Background(), realmID, key.AlgorithmRS256)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if ref.KID != published[0].KID() || ref.Algorithm != key.AlgorithmRS256 {
		t.Errorf("want the active key's ref, got %+v", ref)
	}

	// One pair, or the realm publishes a key nothing it holds can sign for.
	public, ok := signer.Public().(*rsa.PublicKey)
	if !ok {
		t.Fatalf("want an rsa public key, got %T", signer.Public())
	}

	stored, ok := published[0].PublicKey().(*rsa.PublicKey)
	if !ok {
		t.Fatalf("want a stored rsa public key, got %T", published[0].PublicKey())
	}

	if !public.Equal(stored) {
		t.Error("want one key pair, got two")
	}
}

func TestSignerForReportsNoActiveKey(t *testing.T) {
	keys, _, _, _ := newKeyService(t)

	_, _, err := keys.SignerFor(context.Background(), uuid.New(), key.AlgorithmRS256)
	if !errors.Is(err, key.ErrNoActiveKey) {
		t.Fatalf("want ErrNoActiveKey, got %v", err)
	}
}

func TestRewrapMovesRowsToTheCurrentKEK(t *testing.T) {
	keys, store, sealer, _ := newKeyService(t)

	realmID := uuid.New()

	if _, err := keys.EnsureKeys(context.Background(), realmID); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// The master key changed; the fake still opens rows sealed under the old one.
	sealer.rotateTo("kek-next")

	moved, err := keys.Rewrap(context.Background(), 0)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if moved != 2 {
		t.Errorf("want 2 rows moved, got %d", moved)
	}

	for _, stored := range store.keys.byID {
		if stored.KEKID() != "kek-next" {
			t.Errorf("want kek-next, got %q", stored.KEKID())
		}
	}

	// And the material still opens: a rewrap that loses a key is worse than none.
	if _, _, err := keys.SignerFor(context.Background(), realmID, key.AlgorithmES256); err != nil {
		t.Errorf("want the rewrapped key to open, got %v", err)
	}
}

func TestRewrapIsIdempotent(t *testing.T) {
	keys, _, sealer, _ := newKeyService(t)

	if _, err := keys.EnsureKeys(context.Background(), uuid.New()); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	sealer.rotateTo("kek-next")

	if _, err := keys.Rewrap(context.Background(), 0); err != nil {
		t.Fatalf("first rewrap: %v", err)
	}

	moved, err := keys.Rewrap(context.Background(), 0)
	if err != nil {
		t.Fatalf("second rewrap: %v", err)
	}

	if moved != 0 {
		t.Errorf("want nothing left to move, got %d", moved)
	}
}

// --- fakes ---

func newKeyService(t *testing.T) (*service.KeyService, *fakeStore, *fakeSealer, *fakeGenerator) {
	t.Helper()

	sealer := &fakeSealer{kekID: testKEKID}
	generator := &fakeGenerator{}
	store := &fakeStore{repo: newFakeRepo(), keys: newFakeKeys()}

	return service.NewKeyService(store, sealer, generator), store, sealer, generator
}

// fakeSealer is not encryption: it concatenates the aad and the plaintext, so a
// test can assert which aad was used.
type fakeSealer struct {
	kekID   string
	lastAAD []byte
	retired []string
}

func (f *fakeSealer) rotateTo(id string) {
	f.retired = append(f.retired, f.kekID)
	f.kekID = id
}

func (f *fakeSealer) CurrentKEKID() string { return f.kekID }

func (f *fakeSealer) Seal(plaintext, aad []byte) ([]byte, string, error) {
	f.lastAAD = append([]byte(nil), aad...)

	blob := make([]byte, 0, len(aad)+1+len(plaintext))
	blob = append(blob, aad...)
	// The aad is a uuid and a base64url thumbprint, so no zero byte appears in it.
	blob = append(blob, 0x00)
	blob = append(blob, plaintext...)

	return blob, f.kekID, nil
}

func (f *fakeSealer) Open(sealed, aad []byte, kekID string) ([]byte, error) {
	if kekID != f.kekID && !slices.Contains(f.retired, kekID) {
		return nil, errors.New("fake sealer: unknown kek id")
	}

	cut := bytes.IndexByte(sealed, 0x00)
	if cut < 0 || !bytes.Equal(sealed[:cut], aad) {
		return nil, errors.New("fake sealer: the aad does not match")
	}

	return sealed[cut+1:], nil
}

// fakeGenerator keeps RSA-2048 out of the unit suite: 1024 is the smallest Go
// still generates, and nothing in the domain validates key size.
type fakeGenerator struct{ calls int }

func (f *fakeGenerator) Generate(algorithm key.Algorithm) (crypto.Signer, []byte, error) {
	f.calls++

	switch algorithm {
	case key.AlgorithmRS256:
		private, err := rsa.GenerateKey(rand.Reader, 1024)
		if err != nil {
			return nil, nil, err
		}

		pkcs8, err := x509.MarshalPKCS8PrivateKey(private)

		return private, pkcs8, err
	case key.AlgorithmES256:
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}

		pkcs8, err := x509.MarshalPKCS8PrivateKey(private)

		return private, pkcs8, err
	}

	return nil, nil, key.ErrUnsupportedAlgorithm
}

// fakeKeys mimics the two unique indexes: the service's race handling is what
// depends on them firing.
type fakeKeys struct {
	byID  map[uuid.UUID]*key.Key
	order []uuid.UUID

	// Runs once, immediately before the next insert, which stages a lost race.
	onNextCreate func() error

	// Fails the next insert without touching the map.
	failNextCreate error
}

func newFakeKeys() *fakeKeys {
	return &fakeKeys{byID: map[uuid.UUID]*key.Key{}}
}

func (f *fakeKeys) Create(_ context.Context, k *key.Key) error {
	if err := f.failNextCreate; err != nil {
		f.failNextCreate = nil

		return err
	}

	if hook := f.onNextCreate; hook != nil {
		f.onNextCreate = nil

		if err := hook(); err != nil {
			return err
		}
	}

	return f.insert(k)
}

func (f *fakeKeys) insert(k *key.Key) error {
	for _, existing := range f.byID {
		if existing.RealmID() != k.RealmID() {
			continue
		}

		if existing.KID() == k.KID() {
			return key.ErrKIDTaken
		}

		sameSlot := existing.Purpose() == k.Purpose() && existing.Algorithm() == k.Algorithm()
		if sameSlot && existing.Status() == key.StatusActive && k.Status() == key.StatusActive {
			return key.ErrActiveKeyExists
		}
	}

	f.byID[k.ID()] = k
	f.order = append(f.order, k.ID())

	return nil
}

func (f *fakeKeys) FindActive(
	_ context.Context, realmID uuid.UUID, purpose key.Purpose, algorithm key.Algorithm,
) (*key.Key, error) {
	for _, candidate := range f.byID {
		if candidate.RealmID() == realmID &&
			candidate.Purpose() == purpose &&
			candidate.Algorithm() == algorithm &&
			candidate.Status() == key.StatusActive {
			return candidate, nil
		}
	}

	return nil, key.ErrNoActiveKey
}

func (f *fakeKeys) FindByKID(_ context.Context, realmID uuid.UUID, kid string) (*key.Key, error) {
	for _, candidate := range f.byID {
		if candidate.RealmID() == realmID && candidate.KID() == kid {
			return candidate, nil
		}
	}

	return nil, key.ErrNotFound
}

func (f *fakeKeys) ListByRealm(
	_ context.Context, realmID uuid.UUID, statuses []key.Status,
) ([]*key.Key, error) {
	var found []*key.Key

	// Reverse insertion order stands in for id descending: the ids are UUIDv7.
	for index := len(f.order) - 1; index >= 0; index-- {
		candidate := f.byID[f.order[index]]

		if candidate.RealmID() != realmID {
			continue
		}

		if len(statuses) > 0 && !slices.Contains(statuses, candidate.Status()) {
			continue
		}

		found = append(found, candidate)
	}

	return found, nil
}

func (f *fakeKeys) Update(_ context.Context, k *key.Key) error {
	f.byID[k.ID()] = k

	return nil
}

// The store replaces the aggregate either way; what differs between the two is
// which columns the real repository writes, which a map cannot express.
func (f *fakeKeys) Rewrap(ctx context.Context, k *key.Key) error {
	return f.Update(ctx, k)
}

func (f *fakeKeys) ListStaleKEK(_ context.Context, kekID string, limit int) ([]*key.Key, error) {
	var found []*key.Key

	for _, id := range f.order {
		if len(found) == limit {
			break
		}

		if candidate := f.byID[id]; candidate.KEKID() != kekID {
			found = append(found, candidate)
		}
	}

	return found, nil
}
