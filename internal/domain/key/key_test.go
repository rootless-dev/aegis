package key_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
)

func TestNewDerivesTheKID(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	built := newKey(t, key.AlgorithmES256, &private.PublicKey)

	want, err := key.Thumbprint(&private.PublicKey)
	if err != nil {
		t.Fatalf("computing the thumbprint: %v", err)
	}

	if built.KID() != want {
		t.Errorf("want kid %q, got %q", want, built.KID())
	}
}

func TestNewIsBornActive(t *testing.T) {
	built := newECKey(t)

	if built.Status() != key.StatusActive {
		t.Errorf("want active, got %q", built.Status())
	}
}

func TestNewStampsBothTimestampsTogether(t *testing.T) {
	built := newECKey(t)

	if !built.CreatedAt().Equal(built.UpdatedAt()) {
		t.Errorf("want one instant, got %v and %v", built.CreatedAt(), built.UpdatedAt())
	}

	if built.CreatedAt().Location() != time.UTC {
		t.Errorf("want UTC, got %v", built.CreatedAt().Location())
	}
}

func TestNewRefusesAMismatchedAlgorithm(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	_, err = key.New(uuid.New(), key.PurposeSignature, key.AlgorithmES256, &private.PublicKey, []byte("sealed"), "kek")
	if err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestNewRefusesTheNilRealm(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	_, err = key.New(uuid.Nil, key.PurposeSignature, key.AlgorithmES256, &private.PublicKey, []byte("sealed"), "kek")
	if err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestNewRefusesEmptySealedMaterial(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	_, err = key.New(uuid.New(), key.PurposeSignature, key.AlgorithmES256, &private.PublicKey, nil, "kek")
	if err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestNewRefusesAnEmptyKEKID(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	_, err = key.New(uuid.New(), key.PurposeSignature, key.AlgorithmES256, &private.PublicKey, []byte("sealed"), "")
	if err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestRehydrateTakesTheStoredKID(t *testing.T) {
	built := newECKey(t)

	der, err := built.PublicKeyDER()
	if err != nil {
		t.Fatalf("marshalling the public key: %v", err)
	}

	// A kid the thumbprint would never produce, so a recomputing Rehydrate
	// would fail this.
	back, err := key.Rehydrate(
		built.ID(), built.RealmID(), "stored-kid",
		key.PurposeSignature, key.AlgorithmES256, key.StatusPassive,
		der, []byte("sealed"), "kek",
		built.CreatedAt(), built.UpdatedAt(),
	)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if back.KID() != "stored-kid" {
		t.Errorf("want the stored kid, got %q", back.KID())
	}
}

func TestRehydrateRefusesAnUnparseablePublicKey(t *testing.T) {
	_, err := key.Rehydrate(
		uuid.New(), uuid.New(), "kid",
		key.PurposeSignature, key.AlgorithmES256, key.StatusActive,
		[]byte("not der"), []byte("sealed"), "kek",
		time.Now().UTC(), time.Now().UTC(),
	)
	if err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestDeactivateMovesActiveToPassive(t *testing.T) {
	built := newECKey(t)

	if err := built.Deactivate(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if built.Status() != key.StatusPassive {
		t.Errorf("want passive, got %q", built.Status())
	}
}

func TestDeactivateTouchesUpdatedAt(t *testing.T) {
	built := newECKey(t)
	before := built.UpdatedAt()

	if err := built.Deactivate(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if !built.UpdatedAt().After(before) && !built.UpdatedAt().Equal(before) {
		t.Errorf("want updated_at moved forward, got %v then %v", before, built.UpdatedAt())
	}

	if !built.CreatedAt().Equal(before) {
		t.Error("want created_at untouched, got it moved")
	}
}

func TestDisableRefusesAnActiveKey(t *testing.T) {
	built := newECKey(t)

	err := built.Disable()
	if !errors.Is(err, key.ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}

	if built.Status() != key.StatusActive {
		t.Errorf("want the status unchanged, got %q", built.Status())
	}
}

func TestDisableAcceptsAPassiveKey(t *testing.T) {
	built := newECKey(t)

	if err := built.Deactivate(); err != nil {
		t.Fatalf("deactivating: %v", err)
	}

	if err := built.Disable(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if built.Status() != key.StatusDisabled {
		t.Errorf("want disabled, got %q", built.Status())
	}
}

func TestEnableReturnsADisabledKeyToPassive(t *testing.T) {
	built := newECKey(t)

	if err := built.Deactivate(); err != nil {
		t.Fatalf("deactivating: %v", err)
	}

	if err := built.Disable(); err != nil {
		t.Fatalf("disabling: %v", err)
	}

	if err := built.Enable(); err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if built.Status() != key.StatusPassive {
		t.Errorf("want passive, got %q", built.Status())
	}
}

func TestEnableRefusesAPassiveKey(t *testing.T) {
	built := newECKey(t)

	if err := built.Deactivate(); err != nil {
		t.Fatalf("deactivating: %v", err)
	}

	if err := built.Enable(); !errors.Is(err, key.ErrInvalidTransition) {
		t.Fatalf("want ErrInvalidTransition, got %v", err)
	}
}

func TestPublishedFollowsTheStatus(t *testing.T) {
	cases := []struct {
		status key.Status
		want   bool
	}{
		{key.StatusActive, true},
		{key.StatusPassive, true},
		{key.StatusDisabled, false},
	}

	for _, testCase := range cases {
		if got := testCase.status.Published(); got != testCase.want {
			t.Errorf("%q: want %v, got %v", testCase.status, testCase.want, got)
		}
	}
}

func TestPublicKeyDERRoundTrips(t *testing.T) {
	built := newECKey(t)

	der, err := built.PublicKeyDER()
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatalf("parsing the der back: %v", err)
	}

	if !key.AlgorithmES256.Matches(parsed) {
		t.Error("want the round trip to produce a P-256 key, got something else")
	}
}

func newECKey(t *testing.T) *key.Key {
	t.Helper()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	return newKey(t, key.AlgorithmES256, &private.PublicKey)
}
