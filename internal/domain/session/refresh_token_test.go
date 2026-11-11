package session_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/session"
)

const (
	tokenTTL = 30 * time.Minute

	// SHA-256 of "abc", from FIPS 180-2.
	hashOfABC = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
)

func newToken(t *testing.T) *session.RefreshToken {
	t.Helper()

	token, err := session.NewRefreshToken(newID(t), newID(t), uuid.Nil, hashOfABC, t0, t0.Add(tokenTTL))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	return token
}

func TestGenerateSecretIsOpaqueAndUnique(t *testing.T) {
	// 32 bytes in unpadded base64url.
	alphabet := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	seen := make(map[string]bool)

	for range 1000 {
		secret, hash, err := session.GenerateSecret()
		if err != nil {
			t.Fatalf("generating: %v", err)
		}

		if !alphabet.MatchString(secret) {
			t.Fatalf("want 43 base64url characters, got %d", len(secret))
		}

		if hash != session.HashSecret(secret) {
			t.Fatal("the hash returned is not the hash of the secret returned")
		}

		if seen[secret] {
			t.Fatal("the same secret came out twice")
		}

		seen[secret] = true
	}
}

func TestHashSecretIsLowercaseHexSHA256(t *testing.T) {
	if got := session.HashSecret("abc"); got != hashOfABC {
		t.Errorf("want %s, got %s", hashOfABC, got)
	}

	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := session.HashSecret(""); got != want {
		t.Errorf("empty input: want %s, got %s", want, got)
	}
}

func TestAGeneratedHashIsAcceptedByTheConstructor(t *testing.T) {
	_, hash, err := session.GenerateSecret()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	if _, err := session.NewRefreshToken(newID(t), newID(t), uuid.Nil, hash, t0, t0.Add(tokenTTL)); err != nil {
		t.Errorf("creating: %v", err)
	}
}

func TestNewRefreshTokenBuildsAnUnusedToken(t *testing.T) {
	realmID, sessionID := newID(t), newID(t)

	token, err := session.NewRefreshToken(realmID, sessionID, uuid.Nil, hashOfABC, t0, t0.Add(tokenTTL))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if token.ID().Version() != 7 {
		t.Errorf("want a UUIDv7, got version %d", token.ID().Version())
	}

	if token.RealmID() != realmID || token.SessionID() != sessionID {
		t.Error("the realm and the session must be the ones given")
	}

	if token.HasParent() || token.ParentID() != uuid.Nil {
		t.Errorf("the first of a chain has no parent, got %v", token.ParentID())
	}

	if token.Hash() != hashOfABC {
		t.Error("the hash must be the one given")
	}

	if !token.CreatedAt().Equal(t0) || !token.ExpiresAt().Equal(t0.Add(tokenTTL)) {
		t.Errorf("want the given instants, got %v and %v", token.CreatedAt(), token.ExpiresAt())
	}

	if token.Used() || token.UsedAt() != nil {
		t.Errorf("a new token must be unused, got %v", token.UsedAt())
	}
}

func TestNewRefreshTokenRemembersItsParent(t *testing.T) {
	parentID := newID(t)

	token, err := session.NewRefreshToken(newID(t), newID(t), parentID, hashOfABC, t0, t0.Add(tokenTTL))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if !token.HasParent() || token.ParentID() != parentID {
		t.Errorf("want parent %v, got %v", parentID, token.ParentID())
	}
}

func TestNewRefreshTokenTruncatesToMicrosecondsInUTC(t *testing.T) {
	zone := time.FixedZone("BRT", -3*60*60)
	now := time.Date(2026, 3, 1, 6, 0, 0, 123456789, zone)

	token, err := session.NewRefreshToken(newID(t), newID(t), uuid.Nil, hashOfABC, now, now.Add(tokenTTL))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if !isMicrosecondUTC(token.CreatedAt()) || !isMicrosecondUTC(token.ExpiresAt()) {
		t.Errorf("want UTC at microsecond precision, got %v and %v", token.CreatedAt(), token.ExpiresAt())
	}

	if want := now.Truncate(time.Microsecond); !token.CreatedAt().Equal(want) {
		t.Errorf("created: want %v, got %v", want, token.CreatedAt())
	}
}

func TestNewRefreshTokenRejectsWhatItCannotHold(t *testing.T) {
	realmID, sessionID := newID(t), newID(t)

	cases := []struct {
		name      string
		realmID   uuid.UUID
		sessionID uuid.UUID
		expiresAt time.Time
		want      error
	}{
		{"nil realm", uuid.Nil, sessionID, t0.Add(tokenTTL), session.ErrRealmInvalid},
		{"nil session", realmID, uuid.Nil, t0.Add(tokenTTL), session.ErrSessionInvalid},
		{"expiry at creation", realmID, sessionID, t0, session.ErrTTLInvalid},
		{"expiry before creation", realmID, sessionID, t0.Add(-time.Microsecond), session.ErrTTLInvalid},
		// It would be stored equal to the creation.
		{"expiry within the same microsecond", realmID, sessionID, t0.Add(999 * time.Nanosecond), session.ErrTTLInvalid},
	}

	for _, tc := range cases {
		_, err := session.NewRefreshToken(tc.realmID, tc.sessionID, uuid.Nil, hashOfABC, t0, tc.expiresAt)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

var malformedHashes = map[string]string{
	"empty":            "",
	"one short":        hashOfABC[:63],
	"one long":         hashOfABC + "0",
	"upper case":       strings.ToUpper(hashOfABC),
	"not hex":          "g" + hashOfABC[1:],
	"inner space":      hashOfABC[:10] + " " + hashOfABC[11:],
	"trailing newline": hashOfABC[:63] + "\n",
	"multi-byte":       hashOfABC[:62] + "é",
	"the secret":       "q83vEjRWeJCrze8SNFZ4kKvN7xI0VniQq83vEjRWeJA",
	"sha-512":          strings.Repeat(hashOfABC, 2),
}

func TestMalformedHashesAreRefused(t *testing.T) {
	for name, hash := range malformedHashes {
		_, err := session.NewRefreshToken(newID(t), newID(t), uuid.Nil, hash, t0, t0.Add(tokenTTL))
		if !errors.Is(err, session.ErrHashInvalid) {
			t.Errorf("%s: want ErrHashInvalid from NewRefreshToken, got %v", name, err)
		}

		_, err = session.RehydrateRefreshToken(
			newID(t), newID(t), newID(t), uuid.Nil, hash, t0, t0.Add(tokenTTL), nil,
		)
		if !errors.Is(err, session.ErrHashInvalid) {
			t.Errorf("%s: want ErrHashInvalid from RehydrateRefreshToken, got %v", name, err)
		}
	}
}

func TestARejectedHashIsNotQuotedInTheError(t *testing.T) {
	for name, hash := range malformedHashes {
		if hash == "" {
			continue
		}

		_, err := session.NewRefreshToken(newID(t), newID(t), uuid.Nil, hash, t0, t0.Add(tokenTTL))
		if err == nil || strings.Contains(err.Error(), strings.TrimSpace(hash)) {
			t.Errorf("%s: the error carries the hash: %v", name, err)
		}
	}
}

func TestRehydrateRefreshTokenRoundTrips(t *testing.T) {
	id, realmID, sessionID, parentID := newID(t), newID(t), newID(t), newID(t)
	zone := time.FixedZone("BRT", -3*60*60)
	createdAt := time.Date(2026, 3, 1, 9, 0, 0, 123456000, zone)
	expiresAt := createdAt.Add(tokenTTL)
	usedAt := createdAt.Add(5 * time.Minute)

	token, err := session.RehydrateRefreshToken(
		id, realmID, sessionID, parentID, hashOfABC, createdAt, expiresAt, &usedAt,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if token.ID() != id || token.RealmID() != realmID || token.SessionID() != sessionID {
		t.Error("the identifiers must be the stored ones")
	}

	if !token.HasParent() || token.ParentID() != parentID || token.Hash() != hashOfABC {
		t.Errorf("want the stored parent and hash, got %v and %q", token.ParentID(), token.Hash())
	}

	if !token.CreatedAt().Equal(createdAt) || !token.ExpiresAt().Equal(expiresAt) {
		t.Errorf("want the stored instants, got %v and %v", token.CreatedAt(), token.ExpiresAt())
	}

	if !token.Used() || token.UsedAt() == nil || !token.UsedAt().Equal(usedAt) {
		t.Fatalf("use: want %v, got %v", usedAt, token.UsedAt())
	}

	if !isMicrosecondUTC(token.CreatedAt()) || !isMicrosecondUTC(token.ExpiresAt()) ||
		!isMicrosecondUTC(*token.UsedAt()) {
		t.Error("every timestamp must come back in UTC at microsecond precision")
	}
}

func TestRehydrateRefreshTokenKeepsAnUnusedTokenUnused(t *testing.T) {
	token, err := session.RehydrateRefreshToken(
		newID(t), newID(t), newID(t), uuid.Nil, hashOfABC, t0, t0.Add(tokenTTL), nil,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if token.Used() || token.UsedAt() != nil || token.HasParent() {
		t.Errorf("want an unused first token, got used at %v with parent %v", token.UsedAt(), token.ParentID())
	}
}

func TestRehydrateRefreshTokenDoesNotKeepTheCallersPointer(t *testing.T) {
	usedAt := t0.Add(time.Minute)

	token, err := session.RehydrateRefreshToken(
		newID(t), newID(t), newID(t), uuid.Nil, hashOfABC, t0, t0.Add(tokenTTL), &usedAt,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	usedAt = usedAt.Add(time.Hour)

	if want := t0.Add(time.Minute); !token.UsedAt().Equal(want) {
		t.Errorf("the use moved with the caller's variable: got %v", token.UsedAt())
	}
}

func TestRehydrateRefreshTokenValidatesWhatItIsGiven(t *testing.T) {
	id, realmID, sessionID := newID(t), newID(t), newID(t)

	cases := []struct {
		name      string
		id        uuid.UUID
		realmID   uuid.UUID
		sessionID uuid.UUID
		expiresAt time.Time
		want      error
	}{
		{"nil id", uuid.Nil, realmID, sessionID, t0.Add(tokenTTL), session.ErrIDInvalid},
		{"nil realm", id, uuid.Nil, sessionID, t0.Add(tokenTTL), session.ErrRealmInvalid},
		{"nil session", id, realmID, uuid.Nil, t0.Add(tokenTTL), session.ErrSessionInvalid},
		{"expiry at creation", id, realmID, sessionID, t0, session.ErrTTLInvalid},
		{"expiry before creation", id, realmID, sessionID, t0.Add(-time.Microsecond), session.ErrTTLInvalid},
	}

	for _, tc := range cases {
		_, err := session.RehydrateRefreshToken(
			tc.id, tc.realmID, tc.sessionID, uuid.Nil, hashOfABC, t0, tc.expiresAt, nil,
		)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestExpiredTreatsTheDeadlineAsExclusive(t *testing.T) {
	token := newToken(t)

	cases := []struct {
		name string
		at   time.Duration
		want bool
	}{
		{"at creation", 0, false},
		{"one tick before the deadline", tokenTTL - time.Microsecond, false},
		{"sub-tick before the deadline", tokenTTL - time.Nanosecond, false},
		{"at the deadline", tokenTTL, true},
		{"after the deadline", tokenTTL + time.Minute, true},
	}

	for _, tc := range cases {
		if got := token.Expired(t0.Add(tc.at)); got != tc.want {
			t.Errorf("%s: want %t, got %t", tc.name, tc.want, got)
		}
	}
}

func TestUseStampsTheInstant(t *testing.T) {
	token := newToken(t)
	zone := time.FixedZone("BRT", -3*60*60)

	if err := token.Use(t0.Add(time.Minute + 789*time.Nanosecond).In(zone)); err != nil {
		t.Fatalf("using: %v", err)
	}

	if !token.Used() || token.UsedAt() == nil {
		t.Fatal("the token must be used")
	}

	if want := t0.Add(time.Minute); !token.UsedAt().Equal(want) || !isMicrosecondUTC(*token.UsedAt()) {
		t.Errorf("want %v in UTC, got %v", want, token.UsedAt())
	}
}

func TestUseTwiceKeepsTheFirstUse(t *testing.T) {
	token := newToken(t)

	if err := token.Use(t0.Add(time.Minute)); err != nil {
		t.Fatalf("using: %v", err)
	}

	if err := token.Use(t0.Add(2 * time.Minute)); !errors.Is(err, session.ErrTokenUsed) {
		t.Errorf("want ErrTokenUsed, got %v", err)
	}

	if want := t0.Add(time.Minute); !token.UsedAt().Equal(want) {
		t.Errorf("the first use was rewritten: got %v", token.UsedAt())
	}
}

func TestUsedAtDoesNotExposeTheAggregate(t *testing.T) {
	token := newToken(t)

	if err := token.Use(t0.Add(time.Minute)); err != nil {
		t.Fatalf("using: %v", err)
	}

	*token.UsedAt() = t0.Add(24 * time.Hour)

	if want := t0.Add(time.Minute); !token.UsedAt().Equal(want) {
		t.Errorf("the use was rewritten through the accessor: got %v", token.UsedAt())
	}
}
