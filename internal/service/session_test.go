package service_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"maps"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
	"github.com/rootless-dev/aegis/internal/domain/identity"
	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/domain/session"
	"github.com/rootless-dev/aegis/internal/domain/token"
	"github.com/rootless-dev/aegis/internal/service"
)

const (
	testKID       = "test-kid"
	testLogin     = "ana@example.com"
	testUserAgent = "aegis-test/1.0"
	testClientIP  = "203.0.113.7"
)

var testClient = service.ClientInfo{IP: testClientIP, UserAgent: testUserAgent}

// token.Sign refuses an RSA key under 2048 bits, so the suite pays for one real
// key, once.
var signingKey = sync.OnceValue(func() *rsa.PrivateKey {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	return private
})

type sessionFixture struct {
	*identityFixture

	sessions *service.SessionService
	signers  *fakeSigners
	clock    *fakeClock
	realm    *realm.Realm
	ana      *identity.Identity
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()

	return newSessionFixtureWith(t, passwordGrantSettings())
}

func newSessionFixtureWith(t *testing.T, settings realm.Settings) *sessionFixture {
	t.Helper()

	base := newIdentityFixture()
	base.store.sessions = newFakeSessions()
	base.store.sessions.tx = base.store.tx
	base.store.refreshTokens = newFakeRefreshTokens()
	base.store.refreshTokens.tx = base.store.tx

	signers := &fakeSigners{tx: base.store.tx, signer: signingKey()}
	clock := &fakeClock{at: time.Date(2026, time.November, 11, 12, 0, 0, 0, time.UTC)}

	sessions := service.NewSessionService(base.store, base.credentials, signers)
	sessions.SetClock(clock.now)

	acme := newTestRealm(t, "acme", settings)

	return &sessionFixture{
		identityFixture: base,
		sessions:        sessions,
		signers:         signers,
		clock:           clock,
		realm:           acme,
		ana:             base.createWithPassword(t, acme.ID(), testLogin, "ana", false),
	}
}

func passwordGrantSettings() realm.Settings {
	settings := realm.DefaultSettings()
	settings.PasswordGrant = true

	return settings
}

func newTestRealm(t *testing.T, slug string, settings realm.Settings) *realm.Realm {
	t.Helper()

	now := time.Now()

	built, err := realm.Rehydrate(
		uuid.New(), slug, slug, "https://idp.example.com/realms/"+slug, realm.StatusActive, settings, now, now,
	)
	if err != nil {
		t.Fatalf("building realm %s: %v", slug, err)
	}

	return built
}

// reconfigured is the same realm as a later request would load it.
func reconfigured(t *testing.T, r *realm.Realm, status realm.Status, settings realm.Settings) *realm.Realm {
	t.Helper()

	built, err := realm.Rehydrate(
		r.ID(), r.Slug(), r.DisplayName(), r.Issuer(), status, settings, r.CreatedAt(), r.UpdatedAt(),
	)
	if err != nil {
		t.Fatalf("rebuilding realm %s: %v", r.Slug(), err)
	}

	return built
}

func (f *sessionFixture) login(t *testing.T) *service.Tokens {
	t.Helper()

	return f.loginTo(t, f.realm, testLogin)
}

func (f *sessionFixture) loginTo(t *testing.T, r *realm.Realm, login string) *service.Tokens {
	t.Helper()

	tokens, err := f.sessions.Login(t.Context(), r, login, testPassword, testClient)
	if err != nil {
		t.Fatalf("logging in: %v", err)
	}

	return tokens
}

func (f *sessionFixture) refresh(t *testing.T, refreshToken string) *service.Tokens {
	t.Helper()

	tokens, err := f.sessions.Refresh(t.Context(), f.realm, refreshToken, testClient)
	if err != nil {
		t.Fatalf("refreshing: %v", err)
	}

	return tokens
}

func (f *sessionFixture) refreshError(refreshToken string) error {
	_, err := f.sessions.Refresh(context.Background(), f.realm, refreshToken, testClient)

	return err
}

func (f *sessionFixture) storedSession(t *testing.T, id uuid.UUID) *session.Session {
	t.Helper()

	stored, ok := f.store.sessions.rows[id]
	if !ok {
		t.Fatalf("session %s was not stored", id)
	}

	return stored
}

func (f *sessionFixture) storedToken(t *testing.T, secret string) *session.RefreshToken {
	t.Helper()

	for _, stored := range f.store.refreshTokens.rows {
		if stored.Hash() == session.HashSecret(secret) {
			return stored
		}
	}

	t.Fatal("no refresh token is stored under the hash of that secret")

	return nil
}

func (f *sessionFixture) claims(t *testing.T, compact string) token.Claims {
	t.Helper()

	claims, err := token.Verify(compact, func(kid, alg string) (crypto.PublicKey, error) {
		if kid != testKID || alg != token.AlgRS256 {
			return nil, token.ErrKeyUnknown
		}

		return f.signers.signer.Public(), nil
	}, f.clock.at)
	if err != nil {
		t.Fatalf("verifying the access token: %v", err)
	}

	return claims
}

func (f *sessionFixture) assertNothingWritten(t *testing.T) {
	t.Helper()

	if n := len(f.store.sessions.rows); n != 0 {
		t.Errorf("want no session written, got %d", n)
	}

	if n := len(f.store.refreshTokens.rows); n != 0 {
		t.Errorf("want no refresh token written, got %d", n)
	}
}

func (f *sessionFixture) assertEnded(t *testing.T, id uuid.UUID, reason session.EndReason) {
	t.Helper()

	stored := f.storedSession(t, id)

	if !stored.Ended() {
		t.Fatalf("the session must be ended with reason %s, it is live", reason)
	}

	if stored.EndReason() != reason {
		t.Errorf("end reason: want %s, got %s", reason, stored.EndReason())
	}
}

func TestLoginRefusesTheRealmBeforeAnyPasswordWork(t *testing.T) {
	grantOff := realm.DefaultSettings()

	for name, tc := range map[string]struct {
		status   realm.Status
		settings realm.Settings
		want     error
	}{
		"disabled":                    {realm.StatusDisabled, passwordGrantSettings(), service.ErrRealmDisabled},
		"archived":                    {realm.StatusArchived, passwordGrantSettings(), service.ErrRealmDisabled},
		"password grant off":          {realm.StatusActive, grantOff, service.ErrPasswordGrantDisabled},
		"disabled and the grant off":  {realm.StatusDisabled, grantOff, service.ErrRealmDisabled},
		"disabled with a wrong login": {realm.StatusDisabled, passwordGrantSettings(), service.ErrRealmDisabled},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSessionFixture(t)
			refused := reconfigured(t, f.realm, tc.status, tc.settings)
			hashed, verified := f.hasher.hashCalls, f.hasher.verifyCalls

			_, err := f.sessions.Login(t.Context(), refused, testLogin, testPassword, testClient)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}

			if f.hasher.hashCalls != hashed || f.hasher.verifyCalls != verified {
				t.Error("the hasher must not run for a realm that issues nothing")
			}

			if f.signers.calls != 0 {
				t.Error("no signer may be fetched for a realm that issues nothing")
			}

			f.assertNothingWritten(t)
		})
	}
}

func TestLoginPassesAVerificationFailureThroughAndWritesNothing(t *testing.T) {
	t.Run("wrong password", func(t *testing.T) {
		f := newSessionFixture(t)

		_, err := f.sessions.Login(t.Context(), f.realm, testLogin, "not the password", testClient)
		if !errors.Is(err, credential.ErrMismatch) {
			t.Fatalf("want ErrMismatch, got %v", err)
		}

		f.assertNothingWritten(t)
	})

	t.Run("unknown login", func(t *testing.T) {
		f := newSessionFixture(t)

		_, err := f.sessions.Login(t.Context(), f.realm, "nobody@example.com", testPassword, testClient)
		if !errors.Is(err, credential.ErrMismatch) {
			t.Fatalf("want ErrMismatch, got %v", err)
		}

		f.assertNothingWritten(t)
	})

	t.Run("disabled identity", func(t *testing.T) {
		f := newSessionFixture(t)

		if err := f.identities.SetStatus(t.Context(), f.realm.ID(), f.ana.ID(), identity.StatusDisabled); err != nil {
			t.Fatalf("disabling: %v", err)
		}

		_, err := f.sessions.Login(t.Context(), f.realm, testLogin, testPassword, testClient)
		if !errors.Is(err, identity.ErrDisabled) {
			t.Fatalf("want ErrDisabled, got %v", err)
		}

		if f.signers.calls != 0 {
			t.Error("nothing may be signed for a refused login")
		}

		f.assertNothingWritten(t)
	})
}

func TestLoginOpensOneSessionAndIssuesOnePair(t *testing.T) {
	f := newSessionFixture(t)
	settings := f.realm.Settings()

	tokens := f.login(t)

	if len(f.store.sessions.rows) != 1 || len(f.store.refreshTokens.rows) != 1 {
		t.Fatalf(
			"want one session and one refresh token, got %d and %d",
			len(f.store.sessions.rows), len(f.store.refreshTokens.rows),
		)
	}

	opened := f.storedSession(t, tokens.SessionID)

	if opened.RealmID() != f.realm.ID() || opened.IdentityID() != f.ana.ID() {
		t.Errorf("the session belongs to realm %s and identity %s", opened.RealmID(), opened.IdentityID())
	}

	if opened.IP() != testClientIP || opened.UserAgent() != testUserAgent {
		t.Errorf("client: got ip %q and user agent %q", opened.IP(), opened.UserAgent())
	}

	if !opened.CreatedAt().Equal(f.clock.at) || !opened.LastSeenAt().Equal(f.clock.at) {
		t.Errorf("created %s, last seen %s, want both %s", opened.CreatedAt(), opened.LastSeenAt(), f.clock.at)
	}

	if want := f.clock.at.Add(settings.SessionMaxTTL); !opened.ExpiresAt().Equal(want) {
		t.Errorf("absolute deadline: want %s, got %s", want, opened.ExpiresAt())
	}

	if opened.Ended() {
		t.Error("a fresh session must be live")
	}

	if tokens.AccessTokenTTL != settings.AccessTokenTTL {
		t.Errorf("access token ttl: want %s, got %s", settings.AccessTokenTTL, tokens.AccessTokenTTL)
	}

	if tokens.RefreshTokenTTL != settings.SessionIdleTTL {
		t.Errorf("refresh token ttl: want %s, got %s", settings.SessionIdleTTL, tokens.RefreshTokenTTL)
	}

	if tokens.MustChangePassword {
		t.Error("the password was not marked for replacement")
	}
}

func TestLoginSignsTheClaimsOfTheSession(t *testing.T) {
	f := newSessionFixture(t)

	tokens := f.login(t)
	claims := f.claims(t, tokens.AccessToken)

	if claims.Issuer != f.realm.Issuer() {
		t.Errorf("iss: want %q, got %q", f.realm.Issuer(), claims.Issuer)
	}

	if claims.Subject != f.ana.ID().String() {
		t.Errorf("sub: want %s, got %s", f.ana.ID(), claims.Subject)
	}

	if claims.SessionID != tokens.SessionID.String() {
		t.Errorf("sid: want %s, got %s", tokens.SessionID, claims.SessionID)
	}

	jti, err := uuid.Parse(claims.ID)
	if err != nil || jti.Version() != 7 {
		t.Errorf("jti: want a UUIDv7, got %q", claims.ID)
	}

	if !claims.IssuedAt.Equal(f.clock.at) {
		t.Errorf("iat: want %s, got %s", f.clock.at, claims.IssuedAt)
	}

	if want := f.clock.at.Add(f.realm.Settings().AccessTokenTTL); !claims.ExpiresAt.Equal(want) {
		t.Errorf("exp: want %s, got %s", want, claims.ExpiresAt)
	}

	if f.signers.lastRealm != f.realm.ID() || f.signers.lastAlgorithm != key.AlgorithmRS256 {
		t.Errorf("asked for the %s signer of realm %s", f.signers.lastAlgorithm, f.signers.lastRealm)
	}
}

func TestLoginReportsAPasswordMarkedForReplacement(t *testing.T) {
	f := newSessionFixture(t)
	f.createWithPassword(t, f.realm.ID(), "bia@example.com", "bia", true)

	if tokens := f.loginTo(t, f.realm, "bia"); !tokens.MustChangePassword {
		t.Error("the flag of the verification must reach the caller")
	}
}

func TestLoginStoresOnlyTheHashOfTheRefreshToken(t *testing.T) {
	f := newSessionFixture(t)

	tokens := f.login(t)
	first := f.storedToken(t, tokens.RefreshToken)

	if first.Hash() == tokens.RefreshToken {
		t.Error("the secret itself was stored")
	}

	if first.SessionID() != tokens.SessionID || first.RealmID() != f.realm.ID() {
		t.Errorf("the token belongs to session %s of realm %s", first.SessionID(), first.RealmID())
	}

	if first.HasParent() {
		t.Error("the first token of a chain has no parent")
	}

	if first.Used() {
		t.Error("a fresh token must not be used")
	}

	if want := f.clock.at.Add(f.realm.Settings().SessionIdleTTL); !first.ExpiresAt().Equal(want) {
		t.Errorf("token deadline: want %s, got %s", want, first.ExpiresAt())
	}
}

func TestLoginDoesItsCryptographyOutsideTheTransaction(t *testing.T) {
	f := newSessionFixture(t)

	f.login(t)

	if f.hasher.callsInTx != 0 {
		t.Errorf("the hasher ran %d times inside a transaction", f.hasher.callsInTx)
	}

	if f.signers.calls != 1 || f.signers.signatures != 1 {
		t.Fatalf("want one signer fetched and one signature, got %d and %d", f.signers.calls, f.signers.signatures)
	}

	if f.signers.callsInTx != 0 {
		t.Errorf("the signer was fetched or used %d times inside a transaction", f.signers.callsInTx)
	}

	if f.store.refreshTokens.createsOutsideTx != 0 || f.store.sessions.createsOutsideTx != 0 {
		t.Error("the session and its first token must be written inside a transaction")
	}
}

func TestLoginRollsTheSessionBackWhenTheTokenInsertFails(t *testing.T) {
	f := newSessionFixture(t)
	boom := errors.New("boom")
	f.store.refreshTokens.failNextCreate = boom

	if _, err := f.sessions.Login(t.Context(), f.realm, testLogin, testPassword, testClient); !errors.Is(err, boom) {
		t.Fatalf("want the insert failure, got %v", err)
	}

	f.assertNothingWritten(t)
}

func TestLoginWritesNothingWhenTheRealmCannotSign(t *testing.T) {
	f := newSessionFixture(t)
	f.signers.fail = key.ErrNoActiveKey

	_, err := f.sessions.Login(t.Context(), f.realm, testLogin, testPassword, testClient)
	if !errors.Is(err, key.ErrNoActiveKey) {
		t.Fatalf("want ErrNoActiveKey, got %v", err)
	}

	f.assertNothingWritten(t)
}

func TestLoginRecordsAnUnparseableClientIPAsUnknown(t *testing.T) {
	f := newSessionFixture(t)

	tokens, err := f.sessions.Login(
		t.Context(), f.realm, testLogin, testPassword, service.ClientInfo{IP: "not an address", UserAgent: testUserAgent},
	)
	if err != nil {
		t.Fatalf("a bad client ip must not fail the login: %v", err)
	}

	opened := f.storedSession(t, tokens.SessionID)

	if opened.IP() != "" {
		t.Errorf("want the ip recorded as unknown, got %q", opened.IP())
	}

	if opened.UserAgent() != testUserAgent {
		t.Errorf("the user agent must survive, got %q", opened.UserAgent())
	}
}

func TestRefreshRotatesTheToken(t *testing.T) {
	f := newSessionFixture(t)
	settings := f.realm.Settings()

	first := f.login(t)
	firstClaims := f.claims(t, first.AccessToken)

	f.clock.advance(10 * time.Minute)

	second := f.refresh(t, first.RefreshToken)

	if second.RefreshToken == first.RefreshToken {
		t.Fatal("the refresh token must change")
	}

	if second.SessionID != first.SessionID {
		t.Errorf("the session must stay %s, got %s", first.SessionID, second.SessionID)
	}

	if len(f.store.sessions.rows) != 1 || len(f.store.refreshTokens.rows) != 2 {
		t.Fatalf(
			"want one session and two refresh tokens, got %d and %d",
			len(f.store.sessions.rows), len(f.store.refreshTokens.rows),
		)
	}

	presented := f.storedToken(t, first.RefreshToken)
	if used := presented.UsedAt(); used == nil || !used.Equal(f.clock.at) {
		t.Errorf("the presented token must be consumed at %s, got %v", f.clock.at, used)
	}

	next := f.storedToken(t, second.RefreshToken)

	if next.ParentID() != presented.ID() {
		t.Errorf("parent: want %s, got %s", presented.ID(), next.ParentID())
	}

	if next.Used() {
		t.Error("the new token must not be used")
	}

	if want := f.clock.at.Add(settings.SessionIdleTTL); !next.ExpiresAt().Equal(want) {
		t.Errorf("new token deadline: want %s, got %s", want, next.ExpiresAt())
	}

	if touched := f.storedSession(t, first.SessionID); !touched.LastSeenAt().Equal(f.clock.at) {
		t.Errorf("last seen: want %s, got %s", f.clock.at, touched.LastSeenAt())
	}

	if second.AccessTokenTTL != settings.AccessTokenTTL || second.RefreshTokenTTL != settings.SessionIdleTTL {
		t.Errorf("ttls: got access %s and refresh %s", second.AccessTokenTTL, second.RefreshTokenTTL)
	}

	claims := f.claims(t, second.AccessToken)

	if claims.Subject != firstClaims.Subject || claims.SessionID != firstClaims.SessionID {
		t.Errorf("sub and sid must not change, got %s and %s", claims.Subject, claims.SessionID)
	}

	if claims.ID == firstClaims.ID {
		t.Error("every access token carries its own jti")
	}

	if !claims.IssuedAt.Equal(f.clock.at) {
		t.Errorf("iat: want %s, got %s", f.clock.at, claims.IssuedAt)
	}

	if second.MustChangePassword {
		t.Error("a refresh proves no password, so it reports nothing about one")
	}
}

func TestRefreshCapsBothDeadlinesAtTheSessionEnd(t *testing.T) {
	settings := passwordGrantSettings()
	settings.AccessTokenTTL = 5 * time.Minute
	settings.SessionIdleTTL = 10 * time.Minute
	settings.SessionMaxTTL = 12 * time.Minute

	f := newSessionFixtureWith(t, settings)
	first := f.login(t)
	deadline := f.storedSession(t, first.SessionID).ExpiresAt()

	f.clock.advance(8 * time.Minute)

	second := f.refresh(t, first.RefreshToken)

	if want := 4 * time.Minute; second.AccessTokenTTL != want || second.RefreshTokenTTL != want {
		t.Errorf("want both ttls cut to %s, got %s and %s", want, second.AccessTokenTTL, second.RefreshTokenTTL)
	}

	if exp := f.claims(t, second.AccessToken).ExpiresAt; !exp.Equal(deadline) {
		t.Errorf("exp: want the session deadline %s, got %s", deadline, exp)
	}

	if expires := f.storedToken(t, second.RefreshToken).ExpiresAt(); !expires.Equal(deadline) {
		t.Errorf("refresh token deadline: want %s, got %s", deadline, expires)
	}
}

// exp and iat are whole seconds on the wire, so inside the session's last
// second the two are equal and there is no valid token to sign.
func TestRefreshInTheSessionsLastSecondIsExpiredNotAnInternalError(t *testing.T) {
	settings := passwordGrantSettings()
	settings.SessionIdleTTL = 12 * time.Minute
	settings.SessionMaxTTL = 12 * time.Minute

	f := newSessionFixtureWith(t, settings)
	f.clock.advance(700 * time.Millisecond)

	first := f.login(t)

	f.clock.advance(12*time.Minute - 200*time.Millisecond)

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}

	if f.storedToken(t, first.RefreshToken).Used() {
		t.Error("a refused refresh must not consume the token")
	}
}

func TestRefreshDoesItsCryptographyOutsideTheTransaction(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	f.refresh(t, first.RefreshToken)

	if f.signers.calls != 2 || f.signers.signatures != 2 {
		t.Fatalf("want two signers fetched and two signatures, got %d and %d", f.signers.calls, f.signers.signatures)
	}

	if f.signers.callsInTx != 0 {
		t.Errorf("the signer was fetched or used %d times inside a transaction", f.signers.callsInTx)
	}

	if f.store.refreshTokens.createsOutsideTx != 0 || f.store.refreshTokens.consumesOutsideTx != 0 {
		t.Error("the consume and the insert must share a transaction")
	}
}

func TestRefreshRollsTheConsumeBackWhenTheTokenInsertFails(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)
	boom := errors.New("boom")

	f.clock.advance(time.Minute)
	f.store.refreshTokens.failNextCreate = boom

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, boom) {
		t.Fatalf("want the insert failure, got %v", err)
	}

	if f.storedToken(t, first.RefreshToken).Used() {
		t.Error("the presented token must not stay consumed when nothing replaced it")
	}

	// Or the failure would have cost the user the session.
	f.refresh(t, first.RefreshToken)
}

func TestRefreshRefusesADisabledRealm(t *testing.T) {
	for _, status := range []realm.Status{realm.StatusDisabled, realm.StatusArchived} {
		t.Run(status.String(), func(t *testing.T) {
			f := newSessionFixture(t)
			first := f.login(t)
			refused := reconfigured(t, f.realm, status, f.realm.Settings())

			_, err := f.sessions.Refresh(t.Context(), refused, first.RefreshToken, testClient)
			if !errors.Is(err, service.ErrRealmDisabled) {
				t.Fatalf("want ErrRealmDisabled, got %v", err)
			}

			if f.storedToken(t, first.RefreshToken).Used() {
				t.Error("a refused refresh must not consume the token")
			}
		})
	}
}

func TestRefreshStillWorksOnceThePasswordGrantIsTurnedOff(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	settings := f.realm.Settings()
	settings.PasswordGrant = false
	grantOff := reconfigured(t, f.realm, realm.StatusActive, settings)

	if _, err := f.sessions.Refresh(t.Context(), grantOff, first.RefreshToken, testClient); err != nil {
		t.Fatalf("turning the grant off must not end sessions: %v", err)
	}
}

func TestRefreshRefusesATokenItCannotPlace(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		f := newSessionFixture(t)
		f.login(t)

		if err := f.refreshError("never issued"); !errors.Is(err, session.ErrTokenNotFound) {
			t.Fatalf("want ErrTokenNotFound, got %v", err)
		}
	})

	t.Run("of another realm", func(t *testing.T) {
		f := newSessionFixture(t)
		first := f.login(t)
		other := newTestRealm(t, "other", passwordGrantSettings())

		_, err := f.sessions.Refresh(t.Context(), other, first.RefreshToken, testClient)
		if !errors.Is(err, session.ErrTokenNotFound) {
			t.Fatalf("want ErrTokenNotFound, got %v", err)
		}

		// Presenting it to the wrong realm must not have burned it.
		f.refresh(t, first.RefreshToken)
	})

	t.Run("of a session that is gone", func(t *testing.T) {
		f := newSessionFixture(t)
		first := f.login(t)
		delete(f.store.sessions.rows, first.SessionID)

		if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrTokenNotFound) {
			t.Fatalf("want ErrTokenNotFound, got %v", err)
		}
	})
}

func TestReuseEndsTheSessionAndKillsTheChain(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	f.clock.advance(time.Minute)

	second := f.refresh(t, first.RefreshToken)

	f.clock.advance(time.Minute)

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrReuseDetected) {
		t.Fatalf("want ErrReuseDetected, got %v", err)
	}

	f.assertEnded(t, first.SessionID, session.EndReuse)

	endedAt := f.storedSession(t, first.SessionID).EndedAt()
	if !endedAt.Equal(f.clock.at) {
		t.Errorf("ended at: want %s, got %s", f.clock.at, endedAt)
	}

	f.clock.advance(time.Minute)

	if err := f.refreshError(second.RefreshToken); !errors.Is(err, session.ErrEnded) {
		t.Errorf("the legitimate token of the chain: want ErrEnded, got %v", err)
	}

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrReuseDetected) {
		t.Errorf("a second replay: want ErrReuseDetected, got %v", err)
	}

	if again := f.storedSession(t, first.SessionID).EndedAt(); !again.Equal(*endedAt) {
		t.Errorf("a second replay must not rewrite the end, it moved from %s to %s", endedAt, again)
	}

	if n := len(f.store.refreshTokens.rows); n != 2 {
		t.Errorf("no refusal may issue a token, got %d stored", n)
	}
}

func TestReuseOfAnExpiredTokenStillEndsTheSession(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	f.clock.advance(10 * time.Minute)
	f.refresh(t, first.RefreshToken)

	// Past the token's deadline and past the session's idle deadline.
	f.clock.advance(time.Hour)

	if !f.storedToken(t, first.RefreshToken).Expired(f.clock.at) {
		t.Fatal("the replayed token was meant to be expired by now")
	}

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrReuseDetected) {
		t.Fatalf("want ErrReuseDetected, got %v", err)
	}

	f.assertEnded(t, first.SessionID, session.EndReuse)
}

func TestRefreshRefusesAnExpiredToken(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	// The realm lengthened its idle ttl since: the session is within the new
	// one, the token keeps the deadline it was issued with.
	settings := f.realm.Settings()
	settings.SessionIdleTTL = 2 * time.Hour
	lengthened := reconfigured(t, f.realm, realm.StatusActive, settings)

	f.clock.advance(f.realm.Settings().SessionIdleTTL)

	_, err := f.sessions.Refresh(t.Context(), lengthened, first.RefreshToken, testClient)
	if !errors.Is(err, session.ErrTokenExpired) {
		t.Fatalf("want ErrTokenExpired, got %v", err)
	}

	if f.storedSession(t, first.SessionID).Ended() {
		t.Error("an expired token is not a theft signal")
	}

	if f.storedToken(t, first.RefreshToken).Used() {
		t.Error("a refused refresh must not consume the token")
	}
}

func TestRefreshRefusesAnIdleSession(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	// Exactly the deadline, which is exclusive.
	f.clock.advance(f.realm.Settings().SessionIdleTTL)

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}

	if f.storedSession(t, first.SessionID).Ended() {
		t.Error("expiry is not an end: nothing is written for it")
	}
}

func TestRefreshRefusesASessionPastItsAbsoluteDeadline(t *testing.T) {
	settings := passwordGrantSettings()
	settings.SessionMaxTTL = time.Hour

	f := newSessionFixtureWith(t, settings)
	tokens := f.login(t)

	// Kept alive every 20 minutes, so idleness is never what stops it.
	for range 2 {
		f.clock.advance(20 * time.Minute)

		tokens = f.refresh(t, tokens.RefreshToken)
	}

	f.clock.advance(20 * time.Minute)

	// A realm that has since raised both ttls does not extend a session that
	// was opened under the old maximum.
	settings.SessionIdleTTL = 10 * time.Hour
	settings.SessionMaxTTL = 10 * time.Hour
	raised := reconfigured(t, f.realm, realm.StatusActive, settings)

	_, err := f.sessions.Refresh(t.Context(), raised, tokens.RefreshToken, testClient)
	if !errors.Is(err, session.ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestRefreshRefusesAnEndedSession(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	if err := f.sessions.Revoke(t.Context(), f.realm.ID(), first.SessionID); err != nil {
		t.Fatalf("revoking: %v", err)
	}

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrEnded) {
		t.Fatalf("want ErrEnded, got %v", err)
	}

	f.assertEnded(t, first.SessionID, session.EndRevoked)
}

func TestRefreshRefusesAnIdentityThatMayNoLongerAuthenticate(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		f := newSessionFixture(t)
		first := f.login(t)

		if err := f.identities.SetStatus(t.Context(), f.realm.ID(), f.ana.ID(), identity.StatusDisabled); err != nil {
			t.Fatalf("disabling: %v", err)
		}

		if err := f.refreshError(first.RefreshToken); !errors.Is(err, identity.ErrDisabled) {
			t.Fatalf("want ErrDisabled, got %v", err)
		}

		if f.storedSession(t, first.SessionID).Ended() {
			t.Error("the session is refused, not ended")
		}

		if f.storedToken(t, first.RefreshToken).Used() {
			t.Error("a refused refresh must not consume the token")
		}

		if err := f.identities.SetStatus(t.Context(), f.realm.ID(), f.ana.ID(), identity.StatusActive); err != nil {
			t.Fatalf("enabling: %v", err)
		}

		f.refresh(t, first.RefreshToken)
	})

	t.Run("gone", func(t *testing.T) {
		f := newSessionFixture(t)
		first := f.login(t)
		delete(f.store.identities.rows, f.ana.ID())

		if err := f.refreshError(first.RefreshToken); !errors.Is(err, identity.ErrDisabled) {
			t.Fatalf("want ErrDisabled, got %v", err)
		}
	})
}

func TestRefreshLosingTheRaceEndsTheSessionAndIssuesNothing(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	f.clock.advance(time.Minute)

	// The other request consumes the token after this one has read it unused.
	f.store.refreshTokens.beforeConsume = func() {
		f.store.refreshTokens.markUsed(session.HashSecret(first.RefreshToken), f.clock.at)
	}

	if err := f.refreshError(first.RefreshToken); !errors.Is(err, session.ErrReuseDetected) {
		t.Fatalf("want ErrReuseDetected, got %v", err)
	}

	f.assertEnded(t, first.SessionID, session.EndReuse)

	if n := len(f.store.refreshTokens.rows); n != 1 {
		t.Errorf("the loser must persist no token, got %d stored", n)
	}

	if seen := f.storedSession(t, first.SessionID).LastSeenAt(); seen.Equal(f.clock.at) {
		t.Error("the loser must not touch the session")
	}
}

func TestRevokeEndsTheSession(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	f.clock.advance(time.Minute)

	if err := f.sessions.Revoke(t.Context(), f.realm.ID(), first.SessionID); err != nil {
		t.Fatalf("revoking: %v", err)
	}

	f.assertEnded(t, first.SessionID, session.EndRevoked)

	endedAt := f.storedSession(t, first.SessionID).EndedAt()
	if !endedAt.Equal(f.clock.at) {
		t.Errorf("ended at: want %s, got %s", f.clock.at, endedAt)
	}

	f.clock.advance(time.Minute)

	if err := f.sessions.Revoke(t.Context(), f.realm.ID(), first.SessionID); !errors.Is(err, session.ErrEnded) {
		t.Errorf("a second revoke: want ErrEnded, got %v", err)
	}

	if again := f.storedSession(t, first.SessionID).EndedAt(); !again.Equal(*endedAt) {
		t.Errorf("a second revoke must not rewrite the end, it moved from %s to %s", endedAt, again)
	}
}

func TestRevokeReportsALostRaceAsEnded(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	// Ended by someone else between this request's read and its write.
	f.store.sessions.beforeEnd = func() {
		f.store.sessions.endAll(f.clock.at, session.EndReuse, func(*session.Session) bool { return true })
	}

	if err := f.sessions.Revoke(t.Context(), f.realm.ID(), first.SessionID); !errors.Is(err, session.ErrEnded) {
		t.Fatalf("want ErrEnded, got %v", err)
	}

	f.assertEnded(t, first.SessionID, session.EndReuse)
}

func TestRevokeIsScopedToTheRealm(t *testing.T) {
	f := newSessionFixture(t)
	first := f.login(t)

	if err := f.sessions.Revoke(t.Context(), uuid.New(), first.SessionID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("another realm: want ErrNotFound, got %v", err)
	}

	if err := f.sessions.Revoke(t.Context(), f.realm.ID(), uuid.New()); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("an unknown session: want ErrNotFound, got %v", err)
	}

	if f.storedSession(t, first.SessionID).Ended() {
		t.Error("the session of the other realm must stay live")
	}
}

func TestRevokeAllForIdentityCountsTheLiveSessions(t *testing.T) {
	f := newSessionFixture(t)
	bia := f.createWithPassword(t, f.realm.ID(), "bia@example.com", "bia", false)

	ended := f.login(t)
	live := []*service.Tokens{f.login(t), f.login(t)}
	untouched := f.loginTo(t, f.realm, "bia")

	if err := f.sessions.Revoke(t.Context(), f.realm.ID(), ended.SessionID); err != nil {
		t.Fatalf("revoking: %v", err)
	}

	f.clock.advance(time.Minute)

	if n, err := f.sessions.RevokeAllForIdentity(t.Context(), uuid.New(), f.ana.ID()); err != nil || n != 0 {
		t.Fatalf("another realm: want 0 and no error, got %d and %v", n, err)
	}

	n, err := f.sessions.RevokeAllForIdentity(t.Context(), f.realm.ID(), f.ana.ID())
	if err != nil {
		t.Fatalf("revoking all: %v", err)
	}

	if n != 2 {
		t.Errorf("want the 2 live sessions counted, got %d", n)
	}

	for _, tokens := range live {
		f.assertEnded(t, tokens.SessionID, session.EndRevoked)

		if at := f.storedSession(t, tokens.SessionID).EndedAt(); !at.Equal(f.clock.at) {
			t.Errorf("ended at: want %s, got %s", f.clock.at, at)
		}
	}

	if f.storedSession(t, untouched.SessionID).Ended() {
		t.Errorf("the session of %s must stay live", bia.Email())
	}
}

func TestListByIdentityReturnsEndedSessionsToo(t *testing.T) {
	f := newSessionFixture(t)
	f.createWithPassword(t, f.realm.ID(), "bia@example.com", "bia", false)

	older := f.login(t)
	newer := f.login(t)
	f.loginTo(t, f.realm, "bia")

	if err := f.sessions.Revoke(t.Context(), f.realm.ID(), older.SessionID); err != nil {
		t.Fatalf("revoking: %v", err)
	}

	listed, err := f.sessions.ListByIdentity(t.Context(), f.realm.ID(), f.ana.ID())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	if len(listed) != 2 || listed[0].ID() != newer.SessionID || listed[1].ID() != older.SessionID {
		t.Fatalf("want the 2 sessions of the identity, newest first, got %d", len(listed))
	}

	if !listed[1].Ended() {
		t.Error("an ended session must still be listed")
	}

	other, err := f.sessions.ListByIdentity(t.Context(), uuid.New(), f.ana.ID())
	if err != nil || len(other) != 0 {
		t.Errorf("another realm: want nothing and no error, got %d and %v", len(other), err)
	}
}

func TestARealmLeavingActiveEndsItsSessions(t *testing.T) {
	for _, status := range []realm.Status{realm.StatusDisabled, realm.StatusArchived} {
		t.Run(status.String(), func(t *testing.T) {
			f := newSessionFixture(t)

			base, err := url.Parse("https://idp.example.com")
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}

			realms := realmServiceWith(t, f.store, base)

			leaving := createRealmWithLogin(t, f, realms, "leaving")
			staying := createRealmWithLogin(t, f, realms, "staying")

			ended := f.loginTo(t, leaving, testLogin)
			kept := f.loginTo(t, staying, testLogin)

			if err := realms.SetStatus(t.Context(), leaving.ID(), status); err != nil {
				t.Fatalf("setting %s: %v", status, err)
			}

			f.assertEnded(t, ended.SessionID, session.EndRealmDisabled)

			if f.storedSession(t, kept.SessionID).Ended() {
				t.Error("the sessions of another realm must stay live")
			}

			_, err = f.sessions.Refresh(t.Context(), leaving, ended.RefreshToken, testClient)
			if !errors.Is(err, service.ErrRealmDisabled) {
				t.Errorf("while %s: want ErrRealmDisabled, got %v", status, err)
			}

			if err := realms.SetStatus(t.Context(), leaving.ID(), realm.StatusActive); err != nil {
				t.Fatalf("activating: %v", err)
			}

			_, err = f.sessions.Refresh(t.Context(), leaving, ended.RefreshToken, testClient)
			if !errors.Is(err, session.ErrEnded) {
				t.Errorf("activating again must not bring the session back: want ErrEnded, got %v", err)
			}

			f.assertEnded(t, ended.SessionID, session.EndRealmDisabled)
		})
	}
}

// fakeRepo hands out the pointer it stores, so the returned realm follows every
// later status change.
func createRealmWithLogin(
	t *testing.T, f *sessionFixture, realms *service.RealmService, slug string,
) *realm.Realm {
	t.Helper()

	created, err := realms.Create(t.Context(), slug, slug)
	if err != nil {
		t.Fatalf("creating realm %s: %v", slug, err)
	}

	created.SetPasswordGrant(true)
	f.createWithPassword(t, created.ID(), testLogin, "", false)

	return created
}

type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time          { return c.at }
func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// fakeSigners hands out one real key for every realm, wrapped so a signature
// made inside a transaction is counted like a fetch made inside one.
type fakeSigners struct {
	tx     *txProbe
	signer crypto.Signer
	fail   error

	calls      int
	signatures int
	callsInTx  int

	lastRealm     uuid.UUID
	lastAlgorithm key.Algorithm
}

func (f *fakeSigners) SignerFor(
	_ context.Context, realmID uuid.UUID, algorithm key.Algorithm,
) (crypto.Signer, key.Ref, error) {
	f.calls++
	f.noteTx()
	f.lastRealm = realmID
	f.lastAlgorithm = algorithm

	if f.fail != nil {
		return nil, key.Ref{}, f.fail
	}

	return probedSigner{Signer: f.signer, owner: f}, key.Ref{KID: testKID, Algorithm: algorithm}, nil
}

func (f *fakeSigners) noteTx() {
	if f.tx.open {
		f.callsInTx++
	}
}

type probedSigner struct {
	crypto.Signer

	owner *fakeSigners
}

func (p probedSigner) Sign(random io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	p.owner.signatures++
	p.owner.noteTx()

	return p.Signer.Sign(random, digest, opts)
}

// fakeSessions mimics the realm scoping and the conditional writes: an end
// lands only on a live row, and each write touches only its own columns. Like
// fakeIdentities it stores and returns copies.
type fakeSessions struct {
	tx   *txProbe
	rows map[uuid.UUID]*session.Session

	createsOutsideTx int

	// Runs once, between a revocation's read and its write.
	beforeEnd func()
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{rows: map[uuid.UUID]*session.Session{}}
}

// Stored rows are replaced, never mutated, so a shallow clone is a snapshot.
func (f *fakeSessions) snapshot() func() {
	if f == nil {
		return func() {}
	}

	saved := maps.Clone(f.rows)

	return func() { f.rows = saved }
}

func (f *fakeSessions) Create(_ context.Context, s *session.Session) error {
	if !f.tx.isOpen() {
		f.createsOutsideTx++
	}

	if _, taken := f.rows[s.ID()]; taken {
		return errors.New("fake: duplicate session id")
	}

	f.rows[s.ID()] = copySession(s)

	return nil
}

func (f *fakeSessions) Find(_ context.Context, realmID, id uuid.UUID) (*session.Session, error) {
	stored, ok := f.rows[id]
	if !ok || stored.RealmID() != realmID {
		return nil, session.ErrNotFound
	}

	return copySession(stored), nil
}

func (f *fakeSessions) ListByIdentity(
	_ context.Context, realmID, identityID uuid.UUID, limit int,
) ([]*session.Session, error) {
	if limit <= 0 {
		return nil, service.ErrLimitNotPositive
	}

	var listed []*session.Session

	for _, stored := range f.rows {
		if stored.RealmID() == realmID && stored.IdentityID() == identityID {
			listed = append(listed, copySession(stored))
		}
	}

	// The id is a UUIDv7, so descending by id is newest first.
	slices.SortFunc(listed, func(a, b *session.Session) int {
		left, right := a.ID(), b.ID()

		return bytes.Compare(right[:], left[:])
	})

	if len(listed) > limit {
		listed = listed[:limit]
	}

	return listed, nil
}

func (f *fakeSessions) Touch(_ context.Context, s *session.Session) error {
	stored, ok := f.rows[s.ID()]
	if !ok || stored.RealmID() != s.RealmID() || stored.Ended() {
		return nil
	}

	f.rows[s.ID()] = rebuildSession(stored, s.LastSeenAt(), nil, "")

	return nil
}

func (f *fakeSessions) End(_ context.Context, s *session.Session) (bool, error) {
	if hook := f.beforeEnd; hook != nil {
		f.beforeEnd = nil

		hook()
	}

	stored, ok := f.rows[s.ID()]
	if !ok || stored.RealmID() != s.RealmID() || stored.Ended() {
		return false, nil
	}

	if !s.Ended() {
		return false, errors.New("fake: asked to end a session the aggregate has not ended")
	}

	f.rows[s.ID()] = rebuildSession(stored, stored.LastSeenAt(), s.EndedAt(), s.EndReason())

	return true, nil
}

func (f *fakeSessions) EndAllByIdentity(
	_ context.Context, realmID, identityID uuid.UUID, at time.Time, reason session.EndReason,
) (int64, error) {
	return f.endAll(at, reason, func(s *session.Session) bool {
		return s.RealmID() == realmID && s.IdentityID() == identityID
	}), nil
}

func (f *fakeSessions) EndAllByRealm(
	_ context.Context, realmID uuid.UUID, at time.Time, reason session.EndReason,
) (int64, error) {
	return f.endAll(at, reason, func(s *session.Session) bool { return s.RealmID() == realmID }), nil
}

func (f *fakeSessions) endAll(at time.Time, reason session.EndReason, match func(*session.Session) bool) int64 {
	var ended int64

	for id, stored := range f.rows {
		if stored.Ended() || !match(stored) {
			continue
		}

		f.rows[id] = rebuildSession(stored, stored.LastSeenAt(), &at, reason)
		ended++
	}

	return ended
}

func copySession(s *session.Session) *session.Session {
	return rebuildSession(s, s.LastSeenAt(), s.EndedAt(), s.EndReason())
}

func rebuildSession(
	s *session.Session, lastSeenAt time.Time, endedAt *time.Time, reason session.EndReason,
) *session.Session {
	rebuilt, err := session.Rehydrate(
		s.ID(), s.RealmID(), s.IdentityID(), s.IP(), s.UserAgent(),
		s.CreatedAt(), lastSeenAt, s.ExpiresAt(), endedAt, reason,
	)
	if err != nil {
		panic(err)
	}

	return rebuilt
}

// fakeRefreshTokens mimics the unique index on the hash, the realm scoping and
// the conditional consume. Like fakeIdentities it stores and returns copies.
type fakeRefreshTokens struct {
	tx   *txProbe
	rows map[uuid.UUID]*session.RefreshToken

	createsOutsideTx  int
	consumesOutsideTx int

	// Fails the next insert without touching the map.
	failNextCreate error

	// Runs once, between a refresh's read and its consume.
	beforeConsume func()
}

func newFakeRefreshTokens() *fakeRefreshTokens {
	return &fakeRefreshTokens{rows: map[uuid.UUID]*session.RefreshToken{}}
}

func (f *fakeRefreshTokens) snapshot() func() {
	if f == nil {
		return func() {}
	}

	saved := maps.Clone(f.rows)

	return func() { f.rows = saved }
}

func (f *fakeRefreshTokens) Create(_ context.Context, t *session.RefreshToken) error {
	if !f.tx.isOpen() {
		f.createsOutsideTx++
	}

	if err := f.failNextCreate; err != nil {
		f.failNextCreate = nil

		return err
	}

	for _, existing := range f.rows {
		if existing.Hash() == t.Hash() {
			return errors.New("fake: duplicate refresh token hash")
		}
	}

	f.rows[t.ID()] = copyRefreshToken(t, t.UsedAt())

	return nil
}

func (f *fakeRefreshTokens) FindByHash(
	_ context.Context, realmID uuid.UUID, hash string,
) (*session.RefreshToken, error) {
	for _, stored := range f.rows {
		if stored.RealmID() == realmID && stored.Hash() == hash {
			return copyRefreshToken(stored, stored.UsedAt()), nil
		}
	}

	return nil, session.ErrTokenNotFound
}

func (f *fakeRefreshTokens) Consume(_ context.Context, t *session.RefreshToken) (bool, error) {
	if !f.tx.isOpen() {
		f.consumesOutsideTx++
	}

	if hook := f.beforeConsume; hook != nil {
		f.beforeConsume = nil

		hook()
	}

	if !t.Used() {
		return false, errors.New("fake: asked to consume a token the aggregate has not used")
	}

	stored, ok := f.rows[t.ID()]
	if !ok || stored.RealmID() != t.RealmID() || stored.Used() {
		return false, nil
	}

	f.rows[t.ID()] = copyRefreshToken(stored, t.UsedAt())

	return true, nil
}

// markUsed is the write of a rival request, made behind the service's back.
func (f *fakeRefreshTokens) markUsed(hash string, at time.Time) {
	for id, stored := range f.rows {
		if stored.Hash() == hash {
			f.rows[id] = copyRefreshToken(stored, &at)
		}
	}
}

func copyRefreshToken(t *session.RefreshToken, usedAt *time.Time) *session.RefreshToken {
	copied, err := session.RehydrateRefreshToken(
		t.ID(), t.RealmID(), t.SessionID(), t.ParentID(), t.Hash(), t.CreatedAt(), t.ExpiresAt(), usedAt,
	)
	if err != nil {
		panic(err)
	}

	return copied
}
