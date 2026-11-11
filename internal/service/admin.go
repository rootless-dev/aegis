package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/admin"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/domain/session"
)

// realmID is always the realm the holding identity belongs to, never the realm
// a grant applies to.
type AdminGrantRepository interface {
	// Translates uq_admin_grants_global and uq_admin_grants_target into
	// admin.ErrExists.
	Create(ctx context.Context, g *admin.Grant) error

	ListByIdentity(ctx context.Context, realmID, identityID uuid.UUID) ([]*admin.Grant, error)

	// Every grant held by an identity of the realm, oldest first. Refuses a
	// non-positive limit with ErrLimitNotPositive.
	ListByRealm(ctx context.Context, realmID uuid.UUID, limit int) ([]*admin.Grant, error)

	// Returns admin.ErrNotFound when no row matched.
	Delete(ctx context.Context, realmID, id uuid.UUID) error

	// Reports whether some active identity of the realm holds a global grant
	// and has a password credential: an administrator able to sign in.
	HasGlobalAdministrator(ctx context.Context, realmID uuid.UUID) (bool, error)
}

// BootstrapOutcome is the zero value when an administrator able to sign in
// already existed and nothing was done.
type BootstrapOutcome struct {
	Created bool

	// An identity already held the email without being an administrator able
	// to sign in, and was turned into one in place.
	Completed bool

	// Set when Created or Completed.
	Administrator *identity.Identity
}

type AdminService struct {
	store       Store
	identities  *IdentityService
	credentials *CredentialService
}

func NewAdminService(store Store, identities *IdentityService, credentials *CredentialService) *AdminService {
	return &AdminService{store: store, identities: identities, credentials: credentials}
}

func (s *AdminService) GrantGlobal(ctx context.Context, identityID uuid.UUID) (*admin.Grant, error) {
	return s.grant(ctx, identityID, uuid.Nil)
}

func (s *AdminService) GrantRealm(ctx context.Context, identityID, targetRealmID uuid.UUID) (*admin.Grant, error) {
	if targetRealmID == uuid.Nil {
		return nil, admin.ErrTargetInvalid
	}

	if _, err := s.store.Realms().FindByID(ctx, targetRealmID); err != nil {
		return nil, err
	}

	return s.grant(ctx, identityID, targetRealmID)
}

// An identity of another realm is identity.ErrNotFound: the lookup is scoped to
// the master realm and cannot tell it from one that does not exist.
func (s *AdminService) grant(ctx context.Context, identityID, targetRealmID uuid.UUID) (*admin.Grant, error) {
	master, err := s.master(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := s.identities.FindByID(ctx, master.ID(), identityID); err != nil {
		return nil, err
	}

	granted, err := newGrant(master.ID(), identityID, targetRealmID)
	if err != nil {
		return nil, err
	}

	if err := s.store.AdminGrants().Create(ctx, granted); err != nil {
		return nil, err
	}

	return granted, nil
}

// Revoke takes uuid.Nil as the target to revoke the global grant. Nothing
// protects the last administrator: ResetAdministrator is the way back in.
func (s *AdminService) Revoke(ctx context.Context, identityID, targetRealmID uuid.UUID) error {
	master, err := s.master(ctx)
	if err != nil {
		return err
	}

	held, err := s.store.AdminGrants().ListByIdentity(ctx, master.ID(), identityID)
	if err != nil {
		return err
	}

	for _, g := range held {
		if g.TargetRealmID() == targetRealmID {
			return s.store.AdminGrants().Delete(ctx, master.ID(), g.ID())
		}
	}

	return admin.ErrNotFound
}

// List stops at maxPageSize grants.
func (s *AdminService) List(ctx context.Context) ([]*admin.Grant, error) {
	master, err := s.master(ctx)
	if err != nil {
		return nil, err
	}

	return s.store.AdminGrants().ListByRealm(ctx, master.ID(), maxPageSize)
}

func (s *AdminService) ListByIdentity(ctx context.Context, identityID uuid.UUID) ([]*admin.Grant, error) {
	master, err := s.master(ctx)
	if err != nil {
		return nil, err
	}

	return s.store.AdminGrants().ListByIdentity(ctx, master.ID(), identityID)
}

// Authorize answers "may this identity administer that realm". Every refusal is
// admin.ErrNotAuthorized, so the caller cannot leak which rule failed.
//
// The target's existence and status are not asked: administering a disabled
// realm is how it gets enabled again.
func (s *AdminService) Authorize(ctx context.Context, identityID, targetRealmID uuid.UUID) error {
	if targetRealmID == uuid.Nil {
		return admin.ErrNotAuthorized
	}

	master, err := s.master(ctx)
	if err != nil {
		return err
	}

	found, err := s.identities.FindByID(ctx, master.ID(), identityID)
	if errors.Is(err, identity.ErrNotFound) {
		return admin.ErrNotAuthorized
	}

	if err != nil {
		return err
	}

	if found.Status() != identity.StatusActive {
		return admin.ErrNotAuthorized
	}

	held, err := s.store.AdminGrants().ListByIdentity(ctx, master.ID(), identityID)
	if err != nil {
		return err
	}

	if !slices.ContainsFunc(held, func(g *admin.Grant) bool { return g.Covers(targetRealmID) }) {
		return admin.ErrNotAuthorized
	}

	return nil
}

// Bootstrap creates the first administrator, with a password marked for
// replacement, when the master realm has none able to sign in. In every other
// case the arguments are ignored, unvalidated: this is not a way to reset a
// password.
func (s *AdminService) Bootstrap(ctx context.Context, email, username, password string) (BootstrapOutcome, error) {
	master, err := s.master(ctx)
	if err != nil {
		return BootstrapOutcome{}, err
	}

	exists, err := s.store.AdminGrants().HasGlobalAdministrator(ctx, master.ID())
	if err != nil || exists {
		return BootstrapOutcome{}, err
	}

	created, err := s.create(ctx, master.ID(), email, username, password)
	if err == nil {
		return BootstrapOutcome{Created: true, Administrator: created}, nil
	}

	if !errors.Is(err, identity.ErrEmailTaken) && !errors.Is(err, identity.ErrUsernameTaken) {
		return BootstrapOutcome{}, err
	}

	// Nothing locks the bootstrap, so the collision may be another replica
	// that got there first. Its administrator is adopted.
	exists, checkErr := s.store.AdminGrants().HasGlobalAdministrator(ctx, master.ID())
	if checkErr != nil || exists {
		return BootstrapOutcome{}, checkErr
	}

	// Still none: the email belongs to an identity that cannot administer.
	// Refusing here would fail every boot from now on. A username held by
	// some other identity stays an error: the email says who the
	// administrator is.
	squatter, findErr := s.findByEmail(ctx, master.ID(), email)
	if errors.Is(findErr, identity.ErrNotFound) {
		return BootstrapOutcome{}, err
	}

	if findErr != nil {
		return BootstrapOutcome{}, findErr
	}

	completed, err := s.promote(ctx, squatter, password)
	if err != nil {
		return BootstrapOutcome{}, err
	}

	return BootstrapOutcome{Completed: true, Administrator: completed}, nil
}

// ResetAdministrator makes the identity with that email a global administrator
// with this password, marked for replacement, creating it if absent. On an
// identity that exists the username is ignored.
func (s *AdminService) ResetAdministrator(
	ctx context.Context, email, username, password string,
) (*identity.Identity, error) {
	master, err := s.master(ctx)
	if err != nil {
		return nil, err
	}

	found, err := s.findByEmail(ctx, master.ID(), email)
	if errors.Is(err, identity.ErrNotFound) {
		return s.create(ctx, master.ID(), email, username, password)
	}

	if err != nil {
		return nil, err
	}

	return s.promote(ctx, found, password)
}

// HasAdministrator reports whether some active identity holds the global grant
// and has a password: the condition Bootstrap acts on, without acting.
func (s *AdminService) HasAdministrator(ctx context.Context) (bool, error) {
	master, err := s.master(ctx)
	if err != nil {
		return false, err
	}

	return s.store.AdminGrants().HasGlobalAdministrator(ctx, master.ID())
}

// TemporaryAdministrators lists the holders of any grant whose password is
// still marked for replacement, disabled ones included.
func (s *AdminService) TemporaryAdministrators(ctx context.Context) ([]*identity.Identity, error) {
	master, err := s.master(ctx)
	if err != nil {
		return nil, err
	}

	grants, err := s.store.AdminGrants().ListByRealm(ctx, master.ID(), maxPageSize)
	if err != nil {
		return nil, err
	}

	var temporary []*identity.Identity

	seen := map[uuid.UUID]bool{}

	for _, g := range grants {
		if seen[g.IdentityID()] {
			continue
		}

		seen[g.IdentityID()] = true

		password, err := s.store.Credentials().Find(ctx, master.ID(), g.IdentityID(), credential.TypePassword)
		if errors.Is(err, credential.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if !password.MustChange() {
			continue
		}

		holder, err := s.identities.FindByID(ctx, master.ID(), g.IdentityID())
		if err != nil {
			return nil, err
		}

		temporary = append(temporary, holder)
	}

	return temporary, nil
}

func (s *AdminService) master(ctx context.Context) (*realm.Realm, error) {
	return s.store.Realms().FindBySlug(ctx, masterSlug)
}

func (s *AdminService) findByEmail(ctx context.Context, masterID uuid.UUID, email string) (*identity.Identity, error) {
	normalized, err := identity.NormalizeEmail(email)
	if err != nil {
		return nil, err
	}

	return s.store.Identities().FindByEmail(ctx, masterID, normalized)
}

// create writes the identity, its temporary password and the global grant in
// one transaction: an administrator is never observable half made.
func (s *AdminService) create(
	ctx context.Context, masterID uuid.UUID, email, username, password string,
) (*identity.Identity, error) {
	created, err := identity.New(masterID, email, username)
	if err != nil {
		return nil, err
	}

	prepared, err := s.credentials.prepare(masterID, created.ID(), password, true)
	if err != nil {
		return nil, err
	}

	granted, err := admin.NewGlobal(masterID, created.ID())
	if err != nil {
		return nil, err
	}

	err = s.store.InTx(ctx, func(tx Store) error {
		if err := tx.Identities().Create(ctx, created); err != nil {
			return err
		}

		if err := s.credentials.persist(ctx, tx, prepared); err != nil {
			return err
		}

		return tx.AdminGrants().Create(ctx, granted)
	})
	if err != nil {
		return nil, err
	}

	return created, nil
}

// promote turns an existing identity into a global administrator with a
// temporary password: active, granted, and with every live session ended,
// because whoever held the previous password may still hold a session.
func (s *AdminService) promote(
	ctx context.Context, found *identity.Identity, password string,
) (*identity.Identity, error) {
	masterID, id := found.RealmID(), found.ID()

	prepared, err := s.credentials.prepare(masterID, id, password, true)
	if err != nil {
		return nil, err
	}

	var promoted *identity.Identity

	apply := func(tx Store) error {
		current, err := tx.Identities().FindByID(ctx, masterID, id)
		if err != nil {
			return err
		}

		if current.Status() != identity.StatusActive {
			if err := current.SetStatus(identity.StatusActive); err != nil {
				return err
			}

			if err := tx.Identities().Update(ctx, current); err != nil {
				return err
			}
		}

		if err := s.credentials.replace(ctx, tx, prepared); err != nil {
			return err
		}

		held, err := tx.AdminGrants().ListByIdentity(ctx, masterID, id)
		if err != nil {
			return err
		}

		if !slices.ContainsFunc(held, (*admin.Grant).Global) {
			granted, err := admin.NewGlobal(masterID, id)
			if err != nil {
				return err
			}

			if err := tx.AdminGrants().Create(ctx, granted); err != nil {
				return err
			}
		}

		if _, err := tx.Sessions().EndAllByIdentity(ctx, masterID, id, time.Now().UTC(), session.EndRevoked); err != nil {
			return err
		}

		promoted = current

		return nil
	}

	err = s.store.InTx(ctx, apply)

	// Someone else added the grant between the read and the insert. A unique
	// violation poisons the transaction on some engines, so it cannot be
	// swallowed inside; the second pass finds the grant there.
	if errors.Is(err, admin.ErrExists) {
		err = s.store.InTx(ctx, apply)
	}

	if err != nil {
		return nil, err
	}

	return promoted, nil
}

func newGrant(masterID, identityID, targetRealmID uuid.UUID) (*admin.Grant, error) {
	if targetRealmID == uuid.Nil {
		return admin.NewGlobal(masterID, identityID)
	}

	return admin.NewForRealm(masterID, identityID, targetRealmID)
}
