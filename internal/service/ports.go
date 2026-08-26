// Package service holds the use cases and declares the interfaces they consume.
package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

// ErrLimitNotPositive reports a page size no repository will guess at. It lives
// here, beside the interface that imposes the precondition, because the layers
// that have to recognise it cannot import the implementation.
var ErrLimitNotPositive = errors.New("service: List needs a positive limit")

// RealmQuery is a keyset page over realms, ordered by id ascending.
type RealmQuery struct {
	// The zero UUID sorts before every UUIDv7, so an empty value needs no
	// special case in the query.
	After uuid.UUID

	// Must be positive. RealmService.List is what supplies a default.
	Limit int

	// Empty means every status except archived.
	Status []realm.Status
}

type RealmRepository interface {
	Create(ctx context.Context, r *realm.Realm) error
	FindByID(ctx context.Context, id uuid.UUID) (*realm.Realm, error)

	// Returns archived realms too: creation has to see them, or a burned slug
	// gets handed out again.
	FindBySlug(ctx context.Context, slug string) (*realm.Realm, error)

	// Refuses a non-positive q.Limit with ErrLimitNotPositive rather than
	// inventing a page size; RealmService.List is what supplies a default.
	List(ctx context.Context, q RealmQuery) ([]*realm.Realm, error)

	// Writes display_name, status and updated_at, and no other column.
	Update(ctx context.Context, r *realm.Realm) error

	// Separate from Update because the issuer is immutable by design.
	Reissue(ctx context.Context, id uuid.UUID, issuer string) error
}

// Store is the transactional boundary. The repositories handed to the callback
// are bound to one transaction; the ones on Store itself are not.
type Store interface {
	Realms() RealmRepository
	InTx(ctx context.Context, fn func(Store) error) error
}
