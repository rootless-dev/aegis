// Package sealer encrypts private key material with the installation's master
// key.
package sealer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Domain separation, so a recorded id is never a bare hash of the master key.
const kekIDDomain = "aegis:kek:v1"

const kekIDBytes = 16

type ring struct {
	id   string
	aead cipher.AEAD
}

// Sealer always seals with the current key and opens with whichever key a row's
// recorded kek id names, so a rewrap can run while traffic is being served.
type Sealer struct {
	current  ring
	previous *ring
}

// New takes raw 32-byte keys; previous is nil unless a rewrap is under way.
func New(current, previous []byte) (*Sealer, error) {
	currentRing, err := newRing(current)
	if err != nil {
		return nil, err
	}

	built := &Sealer{current: currentRing}

	if len(previous) == 0 {
		return built, nil
	}

	previousRing, err := newRing(previous)
	if err != nil {
		return nil, err
	}

	if previousRing.id == currentRing.id {
		return nil, errors.New("sealer: the previous master key is the current one, so a rewrap would do nothing")
	}

	built.previous = &previousRing

	return built, nil
}

func newRing(material []byte) (ring, error) {
	// aes.NewCipher is the size check: it rejects anything but 16, 24 or 32 bytes.
	block, err := aes.NewCipher(material)
	if err != nil {
		return ring{}, fmt.Errorf("sealer: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ring{}, fmt.Errorf("sealer: %w", err)
	}

	return ring{id: KEKID(material), aead: aead}, nil
}

// KEKID derives the id a row records instead of taking a configured label: a
// label left behind by a key change would name a key that does not open the row.
func KEKID(material []byte) string {
	sum := sha256.Sum256(append([]byte(kekIDDomain), material...))

	return hex.EncodeToString(sum[:kekIDBytes])
}

func (s *Sealer) CurrentKEKID() string { return s.current.id }

// Seal returns nonce ‖ ciphertext ‖ tag, and the id of the key that sealed it.
func (s *Sealer) Seal(plaintext, aad []byte) ([]byte, string, error) {
	nonce := make([]byte, s.current.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", fmt.Errorf("sealer: reading a nonce: %w", err)
	}

	// Seal appends to its first argument, so the nonce becomes the blob's prefix.
	return s.current.aead.Seal(nonce, nonce, plaintext, aad), s.current.id, nil
}

func (s *Sealer) Open(sealed, aad []byte, kekID string) ([]byte, error) {
	chosen, err := s.ring(kekID)
	if err != nil {
		return nil, err
	}

	size := chosen.aead.NonceSize()
	if len(sealed) < size {
		return nil, errors.New("sealer: the sealed material is shorter than a nonce")
	}

	plaintext, err := chosen.aead.Open(nil, sealed[:size], sealed[size:], aad)
	if err != nil {
		// One error for a wrong key, a wrong aad and a modified ciphertext:
		// telling them apart would report whose guess was closer.
		return nil, errors.New("sealer: the sealed material did not open")
	}

	return plaintext, nil
}

func (s *Sealer) ring(kekID string) (ring, error) {
	switch {
	case kekID == s.current.id:
		return s.current, nil
	case s.previous != nil && kekID == s.previous.id:
		return *s.previous, nil
	}

	return ring{}, fmt.Errorf(
		"sealer: no master key with id %q is loaded. If this row predates a master key change, "+
			"set AEGIS_CRYPTO_MASTER_KEY_PREVIOUS and run `aegisd key rewrap`",
		kekID,
	)
}
