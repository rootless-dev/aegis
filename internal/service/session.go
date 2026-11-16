package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/domain/token"
)

var (
	// A disabled realm issues nothing, refresh included. It keeps serving its
	// JWKS, which is why this is not realm.ErrNotAvailable.
	ErrRealmDisabled = errors.New("service: the realm is disabled")

	ErrPasswordGrantDisabled = errors.New("service: the realm does not allow the password grant")
)

// ClientInfo is what the transport knows about who is asking. Both fields are
// recorded on the session and neither is trusted for a decision.
type ClientInfo struct {
	IP        string
	UserAgent string
}

// Tokens is what a successful login or refresh hands back.
type Tokens struct {
	AccessToken     string
	AccessTokenTTL  time.Duration
	RefreshToken    string
	RefreshTokenTTL time.Duration
	SessionID       uuid.UUID

	// The password that was presented is marked for replacement.
	MustChangePassword bool
}

type SessionRepository interface {
	Create(ctx context.Context, s *session.Session) error

	// Returns session.ErrNotFound, including for a session of another realm.
	Find(ctx context.Context, realmID, id uuid.UUID) (*session.Session, error)

	// Newest first, ended sessions included. Refuses a non-positive limit with
	// ErrLimitNotPositive.
	ListByIdentity(ctx context.Context, realmID, identityID uuid.UUID, limit int) ([]*session.Session, error)

	// Writes last_seen_at on a live session and no other column. Touching an
	// ended or missing session is not an error: there is nothing to record.
	Touch(ctx context.Context, s *session.Session) error

	// Writes ended_at and end_reason, only while the row is still live. Reports
	// false when it was not: the session was already ended, or does not exist.
	End(ctx context.Context, s *session.Session) (bool, error)

	// Both end every live session in their scope and report how many.
	EndAllByIdentity(
		ctx context.Context, realmID, identityID uuid.UUID, at time.Time, reason session.EndReason,
	) (int64, error)
	EndAllByRealm(ctx context.Context, realmID uuid.UUID, at time.Time, reason session.EndReason) (int64, error)
}

type RefreshTokenRepository interface {
	Create(ctx context.Context, t *session.RefreshToken) error

	// Returns session.ErrTokenNotFound, including for a token of another realm.
	FindByHash(ctx context.Context, realmID uuid.UUID, hash string) (*session.RefreshToken, error)

	// Writes used_at, only while it is still NULL. Reports false when it was
	// not: of two requests presenting the same token, exactly one gets true.
	Consume(ctx context.Context, t *session.RefreshToken) (bool, error)
}

// The one algorithm access tokens are signed with.
const accessTokenAlgorithm = key.AlgorithmRS256

type SessionService struct {
	store       Store
	credentials *CredentialService
	signers     SignerSource
	now         func() time.Time
}

func NewSessionService(store Store, credentials *CredentialService, signers SignerSource) *SessionService {
	return &SessionService{store: store, credentials: credentials, signers: signers, now: time.Now}
}

// Login is the password grant: it proves the password, opens a session and
// issues the first pair of tokens.
func (s *SessionService) Login(
	ctx context.Context, r *realm.Realm, login, password string, client ClientInfo,
) (*Tokens, error) {
	if r.Status() != realm.StatusActive {
		return nil, ErrRealmDisabled
	}

	if !r.Settings().PasswordGrant {
		return nil, ErrPasswordGrantDisabled
	}

	verified, mustChange, err := s.credentials.VerifyPassword(ctx, r.ID(), login, password)
	if err != nil {
		return nil, err
	}

	now := s.now()
	maxTTL := r.Settings().SessionMaxTTL

	opened, err := session.New(r.ID(), verified.ID(), now, maxTTL, client.IP, client.UserAgent)
	if errors.Is(err, session.ErrIPInvalid) {
		// The address is whatever the transport saw; recorded as unknown.
		opened, err = session.New(r.ID(), verified.ID(), now, maxTTL, "", client.UserAgent)
	}

	if err != nil {
		return nil, err
	}

	// Before the transaction: signing reads and opens a private key.
	tokens, first, err := s.issue(ctx, r, opened, uuid.Nil, now)
	if err != nil {
		return nil, err
	}

	err = s.store.InTx(ctx, func(tx Store) error {
		if createErr := tx.Sessions().Create(ctx, opened); createErr != nil {
			return createErr
		}

		return tx.RefreshTokens().Create(ctx, first)
	})
	if err != nil {
		return nil, err
	}

	tokens.MustChangePassword = mustChange

	return tokens, nil
}

// Refresh trades a refresh token for a new pair and consumes the one presented.
func (s *SessionService) Refresh(
	ctx context.Context, r *realm.Realm, refreshToken string, _ ClientInfo,
) (*Tokens, error) {
	// The password grant flag is not asked: turning it off stops new logins,
	// it does not end sessions.
	if r.Status() != realm.StatusActive {
		return nil, ErrRealmDisabled
	}

	presented, err := s.store.RefreshTokens().FindByHash(ctx, r.ID(), session.HashSecret(refreshToken))
	if err != nil {
		return nil, err
	}

	found, err := s.store.Sessions().Find(ctx, r.ID(), presented.SessionID())
	if errors.Is(err, session.ErrNotFound) {
		return nil, session.ErrTokenNotFound
	}

	if err != nil {
		return nil, err
	}

	now := s.now()
	idleTTL := r.Settings().SessionIdleTTL

	// Ahead of every expiry check: a replayed token ends the session even when
	// it is also expired.
	if presented.Used() {
		if err := endForReuse(ctx, s.store, found, now); err != nil {
			return nil, err
		}

		return nil, session.ErrReuseDetected
	}

	if err := found.Usable(now, idleTTL); err != nil {
		return nil, err
	}

	if presented.Expired(now) {
		return nil, session.ErrTokenExpired
	}

	if err := s.requireActiveIdentity(ctx, found); err != nil {
		return nil, err
	}

	if err := presented.Use(now); err != nil {
		return nil, err
	}

	found.Touch(now)

	tokens, next, err := s.issue(ctx, r, found, presented.ID(), now)
	if err != nil {
		return nil, err
	}

	lostRace := false

	err = s.store.InTx(ctx, func(tx Store) error {
		consumed, consumeErr := tx.RefreshTokens().Consume(ctx, presented)
		if consumeErr != nil {
			return consumeErr
		}

		// Another request consumed it since the read above. Returning nil is
		// deliberate: the transaction has to commit for the end to survive, and
		// the end is all it has written.
		if !consumed {
			lostRace = true

			return endForReuse(ctx, tx, found, now)
		}

		if createErr := tx.RefreshTokens().Create(ctx, next); createErr != nil {
			return createErr
		}

		return tx.Sessions().Touch(ctx, found)
	})
	if err != nil {
		return nil, err
	}

	if lostRace {
		return nil, session.ErrReuseDetected
	}

	return tokens, nil
}

func (s *SessionService) ListByIdentity(
	ctx context.Context, realmID, identityID uuid.UUID,
) ([]*session.Session, error) {
	return s.store.Sessions().ListByIdentity(ctx, realmID, identityID, maxPageSize)
}

// Revoke returns session.ErrNotFound, or session.ErrEnded for a session that
// was already over.
func (s *SessionService) Revoke(ctx context.Context, realmID, sessionID uuid.UUID) error {
	found, err := s.store.Sessions().Find(ctx, realmID, sessionID)
	if err != nil {
		return err
	}

	if err := found.End(s.now(), session.EndRevoked); err != nil {
		return err
	}

	ended, err := s.store.Sessions().End(ctx, found)
	if err != nil {
		return err
	}

	if !ended {
		return session.ErrEnded
	}

	return nil
}

func (s *SessionService) RevokeAllForIdentity(ctx context.Context, realmID, identityID uuid.UUID) (int64, error) {
	return s.store.Sessions().EndAllByIdentity(ctx, realmID, identityID, s.now(), session.EndRevoked)
}

// issue builds the pair for a session and persists nothing. The refresh token
// lives until the session's idle deadline, so the caller touches it first.
func (s *SessionService) issue(
	ctx context.Context, r *realm.Realm, of *session.Session, parentID uuid.UUID, now time.Time,
) (*Tokens, *session.RefreshToken, error) {
	secret, hash, err := session.GenerateSecret()
	if err != nil {
		return nil, nil, err
	}

	refreshExpiresAt := of.IdleDeadline(r.Settings().SessionIdleTTL)

	refresh, err := session.NewRefreshToken(r.ID(), of.ID(), parentID, hash, now, refreshExpiresAt)
	if err != nil {
		return nil, nil, err
	}

	signer, ref, err := s.signers.SignerFor(ctx, r.ID(), accessTokenAlgorithm)
	if err != nil {
		return nil, nil, err
	}

	jti, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("service: generating a token identifier: %w", err)
	}

	accessExpiresAt := now.Add(r.Settings().AccessTokenTTL)
	if accessExpiresAt.After(of.ExpiresAt()) {
		accessExpiresAt = of.ExpiresAt()
	}

	// exp and iat go out in whole seconds: a session in its last second has no
	// token left to give.
	if accessExpiresAt.Unix() <= now.Unix() {
		return nil, nil, fmt.Errorf("%w: its last second is running", session.ErrExpired)
	}

	access, err := token.Sign(signer, ref.Algorithm.String(), ref.KID, token.Claims{
		Issuer:    r.Issuer(),
		Subject:   of.IdentityID().String(),
		SessionID: of.ID().String(),
		ID:        jti.String(),
		IssuedAt:  now,
		ExpiresAt: accessExpiresAt,
	})
	if err != nil {
		return nil, nil, err
	}

	return &Tokens{
		AccessToken:     access,
		AccessTokenTTL:  accessExpiresAt.Sub(now),
		RefreshToken:    secret,
		RefreshTokenTTL: refreshExpiresAt.Sub(now),
		SessionID:       of.ID(),
	}, refresh, nil
}

// A disabled identity keeps no session alive, and neither does a deleted one.
func (s *SessionService) requireActiveIdentity(ctx context.Context, of *session.Session) error {
	owner, err := s.store.Identities().FindByID(ctx, of.RealmID(), of.IdentityID())
	if errors.Is(err, identity.ErrNotFound) {
		return identity.ErrDisabled
	}

	if err != nil {
		return err
	}

	if owner.Status() != identity.StatusActive {
		return identity.ErrDisabled
	}

	return nil
}

// endForReuse answers a consumed token presented again, which is the signal of
// theft. A session that is already over is left as it ended.
func endForReuse(ctx context.Context, st Store, of *session.Session, now time.Time) error {
	if of.Ended() {
		return nil
	}

	if err := of.End(now, session.EndReuse); err != nil {
		return err
	}

	_, err := st.Sessions().End(ctx, of)

	return err
}
