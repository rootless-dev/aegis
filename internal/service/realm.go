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

type RealmService struct {
	store Store

	// Derived from once, at creation. After that the stored column is the truth.
	publicBaseURL *url.URL
}

func NewRealmService(store Store, publicBaseURL *url.URL) *RealmService {
	return &RealmService{store: store, publicBaseURL: publicBaseURL}
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

func (s *RealmService) create(ctx context.Context, slug, displayName string) (*realm.Realm, error) {
	created, err := realm.New(slug, displayName, s.publicBaseURL)
	if err != nil {
		return nil, err
	}

	if err := s.store.Realms().Create(ctx, created); err != nil {
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

// EnsureMaster is the boot seed, and the only caller past the reserved slug
// rule. It touches the issuer and nothing else.
//
// A divergent issuer refuses the boot in production; development rewrites it,
// because the public url comes from the listener there and changing the port
// would leave a stale value with no visible symptom.
func (s *RealmService) EnsureMaster(ctx context.Context, development bool) (*realm.Realm, error) {
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
