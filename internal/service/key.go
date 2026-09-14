package service

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/key"
)

// DefaultAlgorithms is what a realm is provisioned with when none are given.
var DefaultAlgorithms = []key.Algorithm{key.AlgorithmRS256, key.AlgorithmES256}

// Small on purpose: each row in a batch holds a decrypted private key in memory.
const defaultRewrapBatch = 50

type RealmKeyRepository interface {
	// Translates uq_realm_keys_active into key.ErrActiveKeyExists and
	// uq_realm_keys_kid into key.ErrKIDTaken.
	Create(ctx context.Context, k *key.Key) error

	// Returns key.ErrNoActiveKey when the realm has none for that algorithm,
	// which several callers treat as ordinary rather than as a failure.
	FindActive(ctx context.Context, realmID uuid.UUID, purpose key.Purpose, algorithm key.Algorithm) (*key.Key, error)

	// Returns key.ErrNotFound.
	FindByKID(ctx context.Context, realmID uuid.UUID, kid string) (*key.Key, error)

	// Ordered id descending, which is chronological because the id is a
	// UUIDv7. An empty statuses slice means every status.
	ListByRealm(ctx context.Context, realmID uuid.UUID, statuses []key.Status) ([]*key.Key, error)

	// Writes status, active_marker and updated_at, and no other column. A write
	// that matched no row returns key.ErrNotFound, never nil.
	Update(ctx context.Context, k *key.Key) error

	// Writes private_key, kek_id and updated_at, and no other column, under the
	// same not-found rule as Update. Separate from Update because sealed
	// material is immutable outside a rewrap.
	Rewrap(ctx context.Context, k *key.Key) error

	// Rows not sealed under kekID, across every realm, capped at limit.
	ListStaleKEK(ctx context.Context, kekID string, limit int) ([]*key.Key, error)
}

type KeyService struct {
	store     Store
	sealer    Sealer
	generator KeyGenerator
}

func NewKeyService(store Store, sealer Sealer, generator KeyGenerator) *KeyService {
	return &KeyService{store: store, sealer: sealer, generator: generator}
}

// Prepare takes no context and touches no database on purpose: generating
// RSA-2048 costs hundreds of milliseconds and must not happen inside a
// transaction. The caller persists the result inside its own.
func (s *KeyService) Prepare(realmID uuid.UUID, algorithms ...key.Algorithm) ([]*key.Key, error) {
	if len(algorithms) == 0 {
		algorithms = DefaultAlgorithms
	}

	prepared := make([]*key.Key, 0, len(algorithms))

	for _, algorithm := range algorithms {
		signer, pkcs8, err := s.generator.Generate(algorithm)
		if err != nil {
			return nil, err
		}

		// Not read off the aggregate: the aad needs the kid and key.New needs
		// the sealed blob, so the kid has to exist before both.
		kid, err := key.Thumbprint(signer.Public())
		if err != nil {
			return nil, err
		}

		sealed, kekID, err := s.sealer.Seal(pkcs8, keyAAD(realmID, kid))
		if err != nil {
			return nil, fmt.Errorf("service: sealing a private key: %w", err)
		}

		built, err := key.New(realmID, key.PurposeSignature, algorithm, signer.Public(), sealed, kekID)
		if err != nil {
			return nil, err
		}

		prepared = append(prepared, built)
	}

	return prepared, nil
}

// persist writes prepared keys inside a transaction the caller owns.
func (s *KeyService) persist(ctx context.Context, st Store, keys []*key.Key) error {
	for _, built := range keys {
		if err := st.Keys().Create(ctx, built); err != nil {
			return err
		}
	}

	return nil
}

// EnsureKeys gives a realm an active key for each algorithm it lacks; it never
// rotates, disables or reconciles. The read comes first, or every boot
// generates key pairs only to discard them.
func (s *KeyService) EnsureKeys(
	ctx context.Context, realmID uuid.UUID, algorithms ...key.Algorithm,
) ([]*key.Key, error) {
	if len(algorithms) == 0 {
		algorithms = DefaultAlgorithms
	}

	missing := make([]key.Algorithm, 0, len(algorithms))

	for _, algorithm := range algorithms {
		_, err := s.store.Keys().FindActive(ctx, realmID, key.PurposeSignature, algorithm)

		switch {
		case err == nil:
			continue
		case errors.Is(err, key.ErrNoActiveKey):
			missing = append(missing, algorithm)
		default:
			return nil, err
		}
	}

	if len(missing) == 0 {
		return s.Published(ctx, realmID)
	}

	// One transaction per algorithm, not one for all of them. Losing the race
	// on the first would roll back the second with it, and the realm would come
	// up missing an algorithm while the boot reported success.
	for _, algorithm := range missing {
		prepared, err := s.Prepare(realmID, algorithm)
		if err != nil {
			return nil, err
		}

		err = s.store.InTx(ctx, func(tx Store) error { return s.persist(ctx, tx, prepared) })

		// Another replica seeded between the read and the insert:
		// uq_realm_keys_active resolves it, and the loser adopts the winning
		// row rather than failing the boot.
		if err != nil && !errors.Is(err, key.ErrActiveKeyExists) {
			return nil, err
		}
	}

	return s.Published(ctx, realmID)
}

// Rotate moves the outgoing key to passive rather than disabling it, so tokens
// already signed stay verifiable until they expire. A realm with no active key
// is not an error: rotation is the recovery path after a mistaken disable.
func (s *KeyService) Rotate(
	ctx context.Context, realmID uuid.UUID, algorithm key.Algorithm,
) (*key.Key, error) {
	prepared, err := s.Prepare(realmID, algorithm)
	if err != nil {
		return nil, err
	}

	replacement := prepared[0]

	err = s.store.InTx(ctx, func(tx Store) error {
		outgoing, findErr := tx.Keys().FindActive(ctx, realmID, key.PurposeSignature, algorithm)

		switch {
		case findErr == nil:
			if deactivateErr := outgoing.Deactivate(); deactivateErr != nil {
				return deactivateErr
			}

			if updateErr := tx.Keys().Update(ctx, outgoing); updateErr != nil {
				return updateErr
			}
		case !errors.Is(findErr, key.ErrNoActiveKey):
			return findErr
		}

		// Two concurrent rotations collide here and one loses, which is the
		// correct outcome: the alternative is two active keys.
		return tx.Keys().Create(ctx, replacement)
	})
	if err != nil {
		return nil, err
	}

	return replacement, nil
}

// Disable takes a key out of the JWKS. An active key is refused: the realm
// would be left with nothing able to sign.
func (s *KeyService) Disable(ctx context.Context, realmID uuid.UUID, kid string) error {
	return s.mutate(ctx, realmID, kid, func(k *key.Key) error { return k.Disable() })
}

// Enable puts a disabled key back into the JWKS as passive; making it active
// again is a rotation.
func (s *KeyService) Enable(ctx context.Context, realmID uuid.UUID, kid string) error {
	return s.mutate(ctx, realmID, kid, func(k *key.Key) error { return k.Enable() })
}

// List is every key of the realm, disabled ones included: whoever is asking has
// to see what there is to enable.
func (s *KeyService) List(ctx context.Context, realmID uuid.UUID) ([]*key.Key, error) {
	return s.store.Keys().ListByRealm(ctx, realmID, nil)
}

// Published is what the JWKS serves: active and passive, never disabled.
func (s *KeyService) Published(ctx context.Context, realmID uuid.UUID) ([]*key.Key, error) {
	return s.store.Keys().ListByRealm(ctx, realmID, key.PublishedStatuses())
}

// SignerFor satisfies SignerSource. No cache: a rotation on another replica
// would leave one stale.
func (s *KeyService) SignerFor(
	ctx context.Context, realmID uuid.UUID, algorithm key.Algorithm,
) (crypto.Signer, key.Ref, error) {
	found, err := s.store.Keys().FindActive(ctx, realmID, key.PurposeSignature, algorithm)
	if err != nil {
		return nil, key.Ref{}, err
	}

	pkcs8, err := s.sealer.Open(found.Sealed(), keyAAD(realmID, found.KID()), found.KEKID())
	if err != nil {
		return nil, key.Ref{}, fmt.Errorf("service: opening the private key of %s: %w", found.KID(), err)
	}

	parsed, err := x509.ParsePKCS8PrivateKey(pkcs8)
	if err != nil {
		return nil, key.Ref{}, fmt.Errorf("service: reading the private key of %s: %w", found.KID(), err)
	}

	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, key.Ref{}, fmt.Errorf("service: the stored %T cannot sign", parsed)
	}

	return signer, found.Ref(), nil
}

// Rewrap moves every row to the current master key, one row at a time and with
// no transaction spanning them: each row records what opens it, so an
// interrupted run leaves a consistent database and rerunning resumes it.
func (s *KeyService) Rewrap(ctx context.Context, batch int) (int, error) {
	if batch <= 0 {
		batch = defaultRewrapBatch
	}

	target := s.sealer.CurrentKEKID()
	moved := 0

	for {
		// No cursor: a row leaves this set the moment it is rewrapped, so
		// asking again from the start both terminates and resumes.
		stale, err := s.store.Keys().ListStaleKEK(ctx, target, batch)
		if err != nil {
			return moved, err
		}

		if len(stale) == 0 {
			return moved, nil
		}

		for _, found := range stale {
			if err := s.rewrapOne(ctx, found); err != nil {
				return moved, err
			}

			moved++
		}
	}
}

func (s *KeyService) rewrapOne(ctx context.Context, found *key.Key) error {
	aad := keyAAD(found.RealmID(), found.KID())

	pkcs8, err := s.sealer.Open(found.Sealed(), aad, found.KEKID())
	if err != nil {
		return fmt.Errorf(
			"service: rewrapping key %s of realm %s: %w", found.KID(), found.RealmID(), err,
		)
	}

	sealed, kekID, err := s.sealer.Seal(pkcs8, aad)
	if err != nil {
		return err
	}

	if err := found.Rewrapped(sealed, kekID); err != nil {
		return err
	}

	return s.store.Keys().Rewrap(ctx, found)
}

func (s *KeyService) mutate(
	ctx context.Context, realmID uuid.UUID, kid string, apply func(*key.Key) error,
) error {
	return s.store.InTx(ctx, func(tx Store) error {
		found, err := tx.Keys().FindByKID(ctx, realmID, kid)
		if err != nil {
			return err
		}

		if err := apply(found); err != nil {
			return err
		}

		return tx.Keys().Update(ctx, found)
	})
}

// keyAAD binds a sealed blob to its realm and its key, so a row copied into
// another realm does not open. The kid binds as tightly as the id would: it is
// a hash of the very key being sealed.
func keyAAD(realmID uuid.UUID, kid string) []byte {
	return []byte(realmID.String() + "|" + kid)
}
