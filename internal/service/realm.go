package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

const (
	masterSlug        = "master"
	masterDisplayName = "Master"

	defaultRealmPageSize = 50
	maxRealmPageSize     = 500
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

	// Writes display_name, status and updated_at, and no other column. A write
	// that matched no row returns realm.ErrNotFound, never nil.
	Update(ctx context.Context, r *realm.Realm) error

	// Separate from Update because the issuer is immutable by design.
	Reissue(ctx context.Context, id uuid.UUID, issuer string) error
}

type RealmService struct {
	store Store

	// Derived from once, at creation. After that the stored column is the truth.
	publicBaseURL *url.URL

	keys *KeyService
}

func NewRealmService(store Store, publicBaseURL *url.URL, keys *KeyService) *RealmService {
	return &RealmService{store: store, publicBaseURL: publicBaseURL, keys: keys}
}

// Create refuses reserved slugs. That is policy about who may claim a name, not
// a property of a well-formed realm — the aggregate accepts them, because the
// seed needs one.
func (s *RealmService) Create(ctx context.Context, slug, displayName string) (*realm.Realm, error) {
	// Before the check, not left to realm.New: the list compares by exact
	// equality, so " admin " would pass the rule and then be stored as admin.
	slug = strings.TrimSpace(slug)

	if realm.IsReservedSlug(slug) {
		return nil, fmt.Errorf("%w: %q", realm.ErrSlugReserved, slug)
	}

	return s.create(ctx, slug, displayName)
}

// create writes the realm and its signing keys in one transaction, so a realm
// that cannot sign is never observable. The keys are generated before the
// transaction opens: none may be held across an RSA-2048 generation.
func (s *RealmService) create(ctx context.Context, slug, displayName string) (*realm.Realm, error) {
	created, err := realm.New(slug, displayName, s.publicBaseURL)
	if err != nil {
		return nil, err
	}

	prepared, err := s.keys.Prepare(created.ID())
	if err != nil {
		return nil, err
	}

	err = s.store.InTx(ctx, func(tx Store) error {
		if createErr := tx.Realms().Create(ctx, created); createErr != nil {
			return createErr
		}

		return s.keys.persist(ctx, tx, prepared)
	})
	if err != nil {
		return nil, err
	}

	return created, nil
}

func (s *RealmService) FindByID(ctx context.Context, id uuid.UUID) (*realm.Realm, error) {
	return s.store.Realms().FindByID(ctx, id)
}

func (s *RealmService) FindBySlug(ctx context.Context, slug string) (*realm.Realm, error) {
	return s.store.Realms().FindBySlug(ctx, slug)
}

// Resolve is how a protocol endpoint turns a slug into a realm, so no endpoint
// has to remember the status rule. FindBySlug keeps the opposite tolerance,
// because creation has to see archived realms.
func (s *RealmService) Resolve(ctx context.Context, slug string) (*realm.Realm, error) {
	found, err := s.store.Realms().FindBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}

	// Disabled is deliberately absent: a disabled realm still serves its JWKS,
	// because refusing revokes nothing already issued.
	if found.Status() == realm.StatusArchived {
		return nil, fmt.Errorf("%w: %s", realm.ErrNotAvailable, slug)
	}

	return found, nil
}

// Clamps rather than rejects: a page size is not worth failing a call over.
func (s *RealmService) List(ctx context.Context, q RealmQuery) ([]*realm.Realm, error) {
	if q.Limit <= 0 {
		q.Limit = defaultRealmPageSize
	}

	if q.Limit > maxRealmPageSize {
		q.Limit = maxRealmPageSize
	}

	if len(q.Status) == 0 {
		q.Status = []realm.Status{realm.StatusActive, realm.StatusDisabled}
	}

	return s.store.Realms().List(ctx, q)
}

func (s *RealmService) Rename(ctx context.Context, id uuid.UUID, displayName string) error {
	return s.mutate(ctx, id, func(r *realm.Realm) error { return r.Rename(displayName) })
}

func (s *RealmService) SetStatus(ctx context.Context, id uuid.UUID, status realm.Status) error {
	return s.mutate(ctx, id, func(r *realm.Realm) error { return r.SetStatus(status) })
}

// Nothing hard-deletes a realm: the row keeps occupying its slug and issuer,
// or cached discovery and live tokens would follow them to a new realm.
func (s *RealmService) Archive(ctx context.Context, id uuid.UUID) error {
	return s.SetStatus(ctx, id, realm.StatusArchived)
}

func (s *RealmService) mutate(ctx context.Context, id uuid.UUID, apply func(*realm.Realm) error) error {
	return s.store.InTx(ctx, func(tx Store) error {
		found, err := tx.Realms().FindByID(ctx, id)
		if err != nil {
			return err
		}

		if err := apply(found); err != nil {
			return err
		}

		return tx.Realms().Update(ctx, found)
	})
}

func (s *RealmService) EnsureMaster(ctx context.Context, development bool) (*realm.Realm, error) {
	master, err := s.ensureMasterRealm(ctx, development)
	if err != nil {
		return nil, err
	}

	// Not left to create: an adopted or reissued master realm reaches this
	// point without having gone through it.
	if _, err := s.keys.EnsureKeys(ctx, master.ID()); err != nil {
		return nil, err
	}

	return master, nil
}

// ensureMasterRealm is the boot seed, and the only caller past the reserved slug
// rule. It touches the issuer and nothing else.
//
// A divergent issuer refuses the boot in production; development rewrites it,
// because the public url comes from the listener there and changing the port
// would leave a stale value with no visible symptom.
func (s *RealmService) ensureMasterRealm(ctx context.Context, development bool) (*realm.Realm, error) {
	found, err := s.store.Realms().FindBySlug(ctx, masterSlug)

	if errors.Is(err, realm.ErrNotFound) {
		created, createErr := s.create(ctx, masterSlug, masterDisplayName)

		// Another replica seeded between the lookup and this insert: migration
		// holds a session lock, the seed does not. Both unique constraints fire
		// at once — same slug, same issuer — and which one an engine names is
		// not something to depend on, so either means the same thing. The
		// adopted row falls through to the same reconciliation, so a replica
		// holding a different public url cannot skip the issuer check.
		if !errors.Is(createErr, realm.ErrSlugTaken) && !errors.Is(createErr, realm.ErrIssuerTaken) {
			return created, createErr
		}

		found, err = s.store.Realms().FindBySlug(ctx, masterSlug)
	}

	if err != nil {
		return nil, err
	}

	// DeriveIssuer rather than New: an aggregate here would generate a UUID and
	// read the clock for one string that is then discarded.
	expected, err := realm.DeriveIssuer(s.publicBaseURL, masterSlug)
	if err != nil {
		return nil, err
	}

	if found.Issuer() == expected {
		return found, nil
	}

	if !development {
		return nil, fmt.Errorf(
			"realm: the master realm was created with issuer %q and this process derives %q from its public url. "+
				"Every client validates the issuer byte for byte, so serving both is not possible. "+
				"Either point the public url back at %q, or, if the move is deliberate, stop every instance and run "+
				"`UPDATE realms SET issuer = '%s' WHERE slug = '%s';` against the database. "+
				"That second path is not free: every token already issued under %q becomes unverifiable, and every "+
				"cached discovery document held by a client is wrong until it is refetched",
			found.Issuer(), expected, found.Issuer(), expected, masterSlug, found.Issuer(),
		)
	}

	if err := s.store.Realms().Reissue(ctx, found.ID(), expected); err != nil {
		return nil, err
	}

	return s.store.Realms().FindBySlug(ctx, masterSlug)
}
