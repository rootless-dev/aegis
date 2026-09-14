package sealer_test

import (
	"bytes"
	"testing"

	"github.com/rootless-dev/aegis/internal/infra/sealer"
)

var (
	currentKey  = []byte("0123456789abcdef0123456789abcdef")
	previousKey = []byte("fedcba9876543210fedcba9876543210")
)

func TestSealAndOpenRoundTrip(t *testing.T) {
	subject, err := sealer.New(currentKey, nil)
	if err != nil {
		t.Fatalf("building the sealer: %v", err)
	}

	plaintext := []byte("a private key, pretend it is der")
	aad := []byte("realm|kid")

	sealed, kekID, err := subject.Seal(plaintext, aad)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if kekID != subject.CurrentKEKID() {
		t.Errorf("want the current kek id, got %q", kekID)
	}

	if bytes.Contains(sealed, plaintext) {
		t.Error("want the plaintext absent from the blob, got it present")
	}

	opened, err := subject.Open(sealed, aad, kekID)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if !bytes.Equal(opened, plaintext) {
		t.Errorf("want %q, got %q", plaintext, opened)
	}
}

func TestSealUsesAFreshNonce(t *testing.T) {
	subject, err := sealer.New(currentKey, nil)
	if err != nil {
		t.Fatalf("building the sealer: %v", err)
	}

	first, _, err := subject.Seal([]byte("same"), []byte("aad"))
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	second, _, err := subject.Seal([]byte("same"), []byte("aad"))
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	// A repeated nonce under GCM leaks the difference between two plaintexts.
	if bytes.Equal(first, second) {
		t.Error("want two different blobs, got one")
	}
}

func TestOpenRefusesTheWrongAAD(t *testing.T) {
	subject, err := sealer.New(currentKey, nil)
	if err != nil {
		t.Fatalf("building the sealer: %v", err)
	}

	sealed, kekID, err := subject.Seal([]byte("secret"), []byte("realm-a|kid"))
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	// The tenancy boundary is held by the cipher, not by a query: a row copied
	// into another realm does not open.
	if _, err := subject.Open(sealed, []byte("realm-b|kid"), kekID); err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestOpenRefusesAModifiedCiphertext(t *testing.T) {
	subject, err := sealer.New(currentKey, nil)
	if err != nil {
		t.Fatalf("building the sealer: %v", err)
	}

	sealed, kekID, err := subject.Seal([]byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	sealed[len(sealed)-1] ^= 0x01

	if _, err := subject.Open(sealed, []byte("aad"), kekID); err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestOpenRefusesAnUnknownKEKID(t *testing.T) {
	subject, err := sealer.New(currentKey, nil)
	if err != nil {
		t.Fatalf("building the sealer: %v", err)
	}

	sealed, _, err := subject.Seal([]byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if _, err := subject.Open(sealed, []byte("aad"), "0000000000000000"); err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestOpenReadsAPreviousKey(t *testing.T) {
	old, err := sealer.New(previousKey, nil)
	if err != nil {
		t.Fatalf("building the old sealer: %v", err)
	}

	sealed, kekID, err := old.Seal([]byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	// What a replica holds mid-rewrap: the new key to write, the old one to
	// still read rows nothing has rewritten yet.
	during, err := sealer.New(currentKey, previousKey)
	if err != nil {
		t.Fatalf("building the sealer: %v", err)
	}

	opened, err := during.Open(sealed, []byte("aad"), kekID)
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}

	if string(opened) != "secret" {
		t.Errorf("want %q, got %q", "secret", opened)
	}

	_, fresh, err := during.Seal([]byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if fresh == kekID {
		t.Error("want new material sealed under the current key, got the previous id")
	}
}

func TestNewRefusesAKeyOfTheWrongSize(t *testing.T) {
	if _, err := sealer.New([]byte("too short"), nil); err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestNewRefusesAPreviousKeyEqualToTheCurrent(t *testing.T) {
	if _, err := sealer.New(currentKey, currentKey); err == nil {
		t.Fatal("want an error, got none")
	}
}

func TestKEKIDIsStableAndKeySpecific(t *testing.T) {
	if sealer.KEKID(currentKey) != sealer.KEKID(currentKey) {
		t.Error("want a stable id, got two values")
	}

	if sealer.KEKID(currentKey) == sealer.KEKID(previousKey) {
		t.Error("want different ids for different keys, got one")
	}
}
