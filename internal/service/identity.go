package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/identity"
)

// IdentityQuery is a keyset page over one realm's identities, ordered by id
// ascending.
type IdentityQuery struct {
	After uuid.UUID

	// Must be positive. IdentityService.List is what supplies a default.
	Limit int

	// Empty means every status.
	Status []identity.Status
}

// Every method takes the realm, so a lookup that forgets the tenant does not
// compile.
type IdentityRepository interface {
	// Translates uq_identities_email into identity.ErrEmailTaken and
	// uq_identities_username into identity.ErrUsernameTaken.
	Create(ctx context.Context, i *identity.Identity) error

	// All three return identity.ErrNotFound, including for an identity that
	// exists in another realm.
	FindByID(ctx context.Context, realmID, id uuid.UUID) (*identity.Identity, error)
	FindByEmail(ctx context.Context, realmID uuid.UUID, email string) (*identity.Identity, error)
	FindByUsername(ctx context.Context, realmID uuid.UUID, username string) (*identity.Identity, error)

	// Refuses a non-positive q.Limit with ErrLimitNotPositive.
	List(ctx context.Context, realmID uuid.UUID, q IdentityQuery) ([]*identity.Identity, error)

	// Writes status and updated_at, and no other column. A write that matched
	// no row returns identity.ErrNotFound, never nil.
	Update(ctx context.Context, i *identity.Identity) error
}

type IdentityService struct {
	store       Store
	credentials *CredentialService
}

func NewIdentityService(store Store, credentials *CredentialService) *IdentityService {
	return &IdentityService{store: store, credentials: credentials}
}

func (s *IdentityService) Create(
	ctx context.Context, realmID uuid.UUID, email, username string,
) (*identity.Identity, error) {
	created, err := identity.New(realmID, email, username)
	if err != nil {
		return nil, err
	}

	if err := s.store.Identities().Create(ctx, created); err != nil {
		return nil, err
	}

	return created, nil
}

// CreateWithPassword writes the identity and its credential in one transaction,
// so an identity that was meant to have a password is never observable without
// it. The hash is computed before the transaction opens.
func (s *IdentityService) CreateWithPassword(
	ctx context.Context, realmID uuid.UUID, email, username, password string, mustChange bool,
) (*identity.Identity, error) {
	created, err := identity.New(realmID, email, username)
	if err != nil {
		return nil, err
	}

	prepared, err := s.credentials.prepare(realmID, created.ID(), password, mustChange)
	if err != nil {
		return nil, err
	}

	err = s.store.InTx(ctx, func(tx Store) error {
		if createErr := tx.Identities().Create(ctx, created); createErr != nil {
			return createErr
		}

		return s.credentials.persist(ctx, tx, prepared)
	})
	if err != nil {
		return nil, err
	}

	return created, nil
}

func (s *IdentityService) FindByID(ctx context.Context, realmID, id uuid.UUID) (*identity.Identity, error) {
	return s.store.Identities().FindByID(ctx, realmID, id)
}

// FindByLogin takes what someone types into a login field: an email when it
// carries an "@", a username otherwise. A malformed one is identity.ErrNotFound.
func (s *IdentityService) FindByLogin(
	ctx context.Context, realmID uuid.UUID, login string,
) (*identity.Identity, error) {
	return findByLogin(ctx, s.store.Identities(), realmID, login)
}

// Clamps rather than rejects, like RealmService.List.
func (s *IdentityService) List(
	ctx context.Context, realmID uuid.UUID, q IdentityQuery,
) ([]*identity.Identity, error) {
	if q.Limit <= 0 {
		q.Limit = defaultPageSize
	}

	if q.Limit > maxPageSize {
		q.Limit = maxPageSize
	}

	return s.store.Identities().List(ctx, realmID, q)
}

func (s *IdentityService) SetStatus(
	ctx context.Context, realmID, id uuid.UUID, status identity.Status,
) error {
	return s.store.InTx(ctx, func(tx Store) error {
		found, err := tx.Identities().FindByID(ctx, realmID, id)
		if err != nil {
			return err
		}

		if err := found.SetStatus(status); err != nil {
			return err
		}

		return tx.Identities().Update(ctx, found)
	})
}

func findByLogin(
	ctx context.Context, identities IdentityRepository, realmID uuid.UUID, login string,
) (*identity.Identity, error) {
	if strings.Contains(login, "@") {
		email, err := identity.NormalizeEmail(login)
		if err != nil {
			return nil, identity.ErrNotFound
		}

		return identities.FindByEmail(ctx, realmID, email)
	}

	// NormalizeUsername accepts the empty string, because a username is
	// optional. Looked up, it would match an identity that has none.
	username, err := identity.NormalizeUsername(login)
	if err != nil || username == "" {
		return nil, identity.ErrNotFound
	}

	return identities.FindByUsername(ctx, realmID, username)
}
