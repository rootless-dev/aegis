// Package service holds the use cases and declares the interfaces they consume.
//
// This file carries only what crosses more than one use case: the transactional
// boundary, and the ports reaching outside the process. A repository interface
// lives beside the service that consumes it, so opening one file shows both the
// use case and the contract it depends on.
package service

import (
	"context"
	"crypto"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
)

// Sealer encrypts private key material with the installation's master key.
type Sealer interface {
	// The returned kekID is recorded on the row: it is what a later Open, or a
	// rewrap, needs.
	Seal(plaintext, aad []byte) (sealed []byte, kekID string, err error)

	// Takes the row's recorded kekID rather than the current one, so rows a
	// rewrap has not reached yet still open.
	Open(sealed, aad []byte, kekID string) ([]byte, error)

	CurrentKEKID() string
}

// KeyGenerator produces a key pair and the PKCS#8 encoding of its private half.
// A port so the unit tests here do not pay a real RSA-2048 generation.
type KeyGenerator interface {
	Generate(algorithm key.Algorithm) (signer crypto.Signer, pkcs8 []byte, err error)
}

// SignerSource hands out the signer for a realm's active key.
type SignerSource interface {
	SignerFor(ctx context.Context, realmID uuid.UUID, algorithm key.Algorithm) (crypto.Signer, key.Ref, error)
}

// Store is the transactional boundary. The repositories handed to the callback
// are bound to one transaction; the ones on Store itself are not.
//
// Accessors only. A method that did work here would be one every fake in the
// test suite has to implement and one no transaction boundary needs.
type Store interface {
	Realms() RealmRepository
	Keys() RealmKeyRepository
	InTx(ctx context.Context, fn func(Store) error) error
}
