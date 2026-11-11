package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
)

type CredentialRepository interface {
	// Translates uq_credentials_type into credential.ErrExists.
	Create(ctx context.Context, c *credential.Credential) error

	// Returns credential.ErrNotFound, including for a credential that exists in
	// another realm.
	Find(ctx context.Context, realmID, identityID uuid.UUID, typ credential.Type) (*credential.Credential, error)

	// Writes hash, must_change, last_used_at and updated_at, and no other
	// column. A write that matched no row returns credential.ErrNotFound.
	Update(ctx context.Context, c *credential.Credential) error

	// The same write as Update, applied only while the stored hash is still
	// previousHash. Reports false when it is not: the password was replaced
	// since c was read, and writing c would put the old one back.
	UpdateIfHash(ctx context.Context, c *credential.Credential, previousHash string) (bool, error)
}

// Bounds what one request can make the hasher chew on. Not a strength rule.
const maxPasswordLength = 1024

type CredentialService struct {
	store  Store
	hasher PasswordHasher

	dummyOnce sync.Once
	dummyHash string
	dummyErr  error
}

func NewCredentialService(store Store, hasher PasswordHasher) *CredentialService {
	return &CredentialService{store: store, hasher: hasher}
}

// SetPassword gives the identity a password, replacing the one it has.
func (s *CredentialService) SetPassword(
	ctx context.Context, realmID, identityID uuid.UUID, password string, mustChange bool,
) error {
	prepared, err := s.prepare(realmID, identityID, password, mustChange)
	if err != nil {
		return err
	}

	return s.store.InTx(ctx, func(tx Store) error {
		if _, findErr := tx.Identities().FindByID(ctx, realmID, identityID); findErr != nil {
			return findErr
		}

		return s.replace(ctx, tx, prepared)
	})
}

// replace writes a prepared password over the one the identity has, or as its
// first, inside a transaction the caller owns.
func (s *CredentialService) replace(ctx context.Context, tx Store, prepared *credential.Credential) error {
	existing, err := tx.Credentials().Find(ctx, prepared.RealmID(), prepared.IdentityID(), credential.TypePassword)
	if errors.Is(err, credential.ErrNotFound) {
		return s.persist(ctx, tx, prepared)
	}

	if err != nil {
		return err
	}

	if err := existing.Replace(prepared.Hash(), prepared.MustChange()); err != nil {
		return err
	}

	return tx.Credentials().Update(ctx, existing)
}

// VerifyPassword answers "is this the password of that login in this realm".
// It creates no session and issues nothing.
//
// Every way of not proving the password is credential.ErrMismatch and costs
// one verification, so neither the error nor the response time tells whether
// the login exists.
func (s *CredentialService) VerifyPassword(
	ctx context.Context, realmID uuid.UUID, login, password string,
) (*identity.Identity, bool, error) {
	if len(password) > maxPasswordLength {
		return nil, false, s.mismatch("")
	}

	found, err := findByLogin(ctx, s.store.Identities(), realmID, login)
	if errors.Is(err, identity.ErrNotFound) {
		return nil, false, s.mismatch(password)
	}

	if err != nil {
		return nil, false, err
	}

	stored, err := s.store.Credentials().Find(ctx, realmID, found.ID(), credential.TypePassword)
	if errors.Is(err, credential.ErrNotFound) {
		return nil, false, s.mismatch(password)
	}

	if err != nil {
		return nil, false, err
	}

	ok, needsRehash, err := s.hasher.Verify(password, stored.Hash())
	if err != nil {
		return nil, false, fmt.Errorf("service: verifying the password of identity %s: %w", found.ID(), err)
	}

	if !ok {
		return nil, false, credential.ErrMismatch
	}

	// Only after the password is proven: asked earlier, this would tell anyone
	// which logins exist and are disabled.
	if found.Status() != identity.StatusActive {
		return nil, false, identity.ErrDisabled
	}

	verified := stored.Hash()

	stored.MarkUsed()

	if needsRehash {
		hash, hashErr := s.hash(password)
		if hashErr != nil {
			return nil, false, hashErr
		}

		if rehashErr := stored.Rehash(hash); rehashErr != nil {
			return nil, false, rehashErr
		}
	}

	// A password replaced since the read wins, and the result is ignored: the
	// one presented was the right one when it was checked.
	if _, err := s.store.Credentials().UpdateIfHash(ctx, stored, verified); err != nil {
		return nil, false, err
	}

	return found, stored.MustChange(), nil
}

// prepare takes no context and touches no database on purpose: argon2id costs
// tens of milliseconds and 64 MiB, and must not happen inside a transaction.
// The caller persists the result inside its own.
func (s *CredentialService) prepare(
	realmID, identityID uuid.UUID, password string, mustChange bool,
) (*credential.Credential, error) {
	if password == "" {
		return nil, fmt.Errorf("%w: it is empty", credential.ErrPasswordInvalid)
	}

	if len(password) > maxPasswordLength {
		return nil, fmt.Errorf(
			"%w: %d bytes, the limit is %d", credential.ErrPasswordInvalid, len(password), maxPasswordLength,
		)
	}

	hash, err := s.hash(password)
	if err != nil {
		return nil, err
	}

	return credential.NewPassword(realmID, identityID, hash, mustChange)
}

// persist writes a prepared credential inside a transaction the caller owns.
func (s *CredentialService) persist(ctx context.Context, st Store, c *credential.Credential) error {
	return st.Credentials().Create(ctx, c)
}

func (s *CredentialService) hash(password string) (string, error) {
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return "", fmt.Errorf("service: hashing a password: %w", err)
	}

	return hash, nil
}

// mismatch is the answer for a login with no stored hash to compare against. It
// verifies against a dummy first, so it takes as long as a wrong password does.
func (s *CredentialService) mismatch(password string) error {
	// Built with the same hasher rather than hardcoded, so it carries the cost
	// parameters real hashes are produced with.
	s.dummyOnce.Do(func() {
		s.dummyHash, s.dummyErr = s.hash(rand.Text())
	})

	if s.dummyErr != nil {
		return s.dummyErr
	}

	if _, _, err := s.hasher.Verify(password, s.dummyHash); err != nil {
		return fmt.Errorf("service: verifying against the dummy hash: %w", err)
	}

	return credential.ErrMismatch
}
