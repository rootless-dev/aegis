package credential_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/credential"
)

const (
	hashA = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$RdescudvJCsgt3ub+b+dWRWJTmaaJObG"
	hashB = "$argon2id$v=19$m=131072,t=4,p=4$b3RoZXJzYWx0$Zm9vYmFyYmF6cXV4Zm9vYmFyYmF6cXV4"
)

func newID(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generating an identifier: %v", err)
	}

	return id
}

func newPassword(t *testing.T, mustChange bool) *credential.Credential {
	t.Helper()

	c, err := credential.NewPassword(newID(t), newID(t), hashA, mustChange)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	return c
}

func isMicrosecondUTC(at time.Time) bool {
	return at.Location() == time.UTC && at.Equal(at.Truncate(time.Microsecond))
}

func TestNewPasswordBuildsAPasswordCredential(t *testing.T) {
	realmID, identityID := newID(t), newID(t)

	c, err := credential.NewPassword(realmID, identityID, hashA, false)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if c.ID().Version() != 7 {
		t.Errorf("want a UUIDv7, got version %d", c.ID().Version())
	}

	if c.RealmID() != realmID || c.IdentityID() != identityID {
		t.Error("the realm and the identity must be the ones given")
	}

	if c.Type() != credential.TypePassword {
		t.Errorf("type: want password, got %q", c.Type())
	}

	if c.Hash() != hashA {
		t.Errorf("hash: got %q", c.Hash())
	}

	if c.LastUsedAt() != nil {
		t.Error("a credential that was never used must have no last use")
	}
}

func TestNewPasswordStampsBothTimestampsTogether(t *testing.T) {
	c := newPassword(t, false)

	if !c.CreatedAt().Equal(c.UpdatedAt()) {
		t.Errorf("want one instant, got %v and %v", c.CreatedAt(), c.UpdatedAt())
	}

	if !isMicrosecondUTC(c.CreatedAt()) || !isMicrosecondUTC(c.UpdatedAt()) {
		t.Errorf("want UTC at microsecond precision, got %v and %v", c.CreatedAt(), c.UpdatedAt())
	}
}

func TestNewPasswordHonoursMustChange(t *testing.T) {
	for _, want := range []bool{true, false} {
		if got := newPassword(t, want).MustChange(); got != want {
			t.Errorf("must change: want %t, got %t", want, got)
		}
	}
}

func TestNewPasswordRejectsMissingOwners(t *testing.T) {
	if _, err := credential.NewPassword(uuid.Nil, newID(t), hashA, false); !errors.Is(err, credential.ErrRealmInvalid) {
		t.Errorf("want ErrRealmInvalid, got %v", err)
	}

	if _, err := credential.NewPassword(newID(t), uuid.Nil, hashA, false); !errors.Is(err, credential.ErrIdentityInvalid) {
		t.Errorf("want ErrIdentityInvalid, got %v", err)
	}
}

var malformedHashes = map[string]string{
	"empty":             "",
	"plaintext":         "correct horse battery staple",
	"no leading dollar": "argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
	"inner space":       "$argon2id$v=19$m=65536, t=3,p=4$c2FsdA$aGFzaA",
	"leading space":     " " + hashA,
	"trailing newline":  hashA + "\n",
	"tab":               "$argon2id\t$v=19",
	"nul":               "$argon2id$v=19\x00",
	"delete":            "$argon2id$v=19\x7f",
	"unicode space":     "$argon2id$v=19 ",
	"too long":          "$" + strings.Repeat("a", 255),
}

func TestNewPasswordRejectsMalformedHashes(t *testing.T) {
	for name, hash := range malformedHashes {
		if _, err := credential.NewPassword(newID(t), newID(t), hash, false); !errors.Is(err, credential.ErrHashInvalid) {
			t.Errorf("%s: want ErrHashInvalid, got %v", name, err)
		}
	}
}

func TestNewPasswordAcceptsAHashAtTheColumnLimit(t *testing.T) {
	for _, hash := range []string{"$", hashA, "$" + strings.Repeat("a", 254)} {
		if _, err := credential.NewPassword(newID(t), newID(t), hash, false); err != nil {
			t.Errorf("a %d byte hash must be accepted, got %v", len(hash), err)
		}
	}
}

// The limit is the column's, and a varchar(255) is refused or truncated by the
// engine depending on the dialect and its mode.
func TestTheHashLimitIsCountedInBytes(t *testing.T) {
	hash := "$" + strings.Repeat("é", 127)

	if _, err := credential.NewPassword(newID(t), newID(t), hash, false); err != nil {
		t.Errorf("255 bytes must be accepted, got %v", err)
	}

	if _, err := credential.NewPassword(newID(t), newID(t), hash+"a", false); !errors.Is(err, credential.ErrHashInvalid) {
		t.Errorf("256 bytes must be refused, got %v", err)
	}
}

// An error ends up in a log line, and a hash is material for an offline attack.
func TestARejectedHashIsNotQuotedInTheError(t *testing.T) {
	_, err := credential.NewPassword(newID(t), newID(t), hashA+" ", false)
	if err == nil {
		t.Fatal("want an error, got none")
	}

	if strings.Contains(err.Error(), hashA) {
		t.Errorf("the error carries the hash: %v", err)
	}
}

func TestRehydrateRoundTrips(t *testing.T) {
	id, realmID, identityID := newID(t), newID(t), newID(t)
	zone := time.FixedZone("BRT", -3*60*60)
	createdAt := time.Date(2026, 3, 1, 9, 0, 0, 123456000, zone)
	updatedAt := createdAt.Add(time.Hour)
	lastUsedAt := createdAt.Add(30 * time.Minute)

	c, err := credential.Rehydrate(
		id, realmID, identityID, credential.TypePassword, hashA, true, &lastUsedAt, createdAt, updatedAt,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if c.ID() != id || c.RealmID() != realmID || c.IdentityID() != identityID {
		t.Error("the identifiers must be the stored ones")
	}

	if c.Type() != credential.TypePassword || c.Hash() != hashA || !c.MustChange() {
		t.Errorf("want the stored type, hash and flag, got %q, %q, %t", c.Type(), c.Hash(), c.MustChange())
	}

	if !c.CreatedAt().Equal(createdAt) || !c.UpdatedAt().Equal(updatedAt) {
		t.Errorf("want the stored instants, got %v and %v", c.CreatedAt(), c.UpdatedAt())
	}

	if c.LastUsedAt() == nil || !c.LastUsedAt().Equal(lastUsedAt) {
		t.Fatalf("last use: want %v, got %v", lastUsedAt, c.LastUsedAt())
	}

	if c.CreatedAt().Location() != time.UTC || c.UpdatedAt().Location() != time.UTC ||
		c.LastUsedAt().Location() != time.UTC {
		t.Error("every timestamp must come back in UTC")
	}
}

func TestRehydrateKeepsANeverUsedCredentialNeverUsed(t *testing.T) {
	now := time.Now().UTC()

	c, err := credential.Rehydrate(
		newID(t), newID(t), newID(t), credential.TypePassword, hashA, false, nil, now, now,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if c.LastUsedAt() != nil {
		t.Errorf("want no last use, got %v", c.LastUsedAt())
	}
}

func TestRehydrateDoesNotKeepTheCallersPointer(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	lastUsedAt := now

	c, err := credential.Rehydrate(
		newID(t), newID(t), newID(t), credential.TypePassword, hashA, false, &lastUsedAt, now, now,
	)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	lastUsedAt = lastUsedAt.Add(time.Hour)

	if !c.LastUsedAt().Equal(now) {
		t.Errorf("last use moved with the caller's variable: got %v", c.LastUsedAt())
	}
}

// Data a customer's DBA has touched is not trusted input.
func TestRehydrateValidatesWhatItIsGiven(t *testing.T) {
	id, realmID, identityID := newID(t), newID(t), newID(t)
	now := time.Now().UTC()

	cases := []struct {
		name       string
		id         uuid.UUID
		realmID    uuid.UUID
		identityID uuid.UUID
		typ        credential.Type
		hash       string
		want       error
	}{
		{"nil id", uuid.Nil, realmID, identityID, credential.TypePassword, hashA, credential.ErrIDInvalid},
		{"nil realm", id, uuid.Nil, identityID, credential.TypePassword, hashA, credential.ErrRealmInvalid},
		{"nil identity", id, realmID, uuid.Nil, credential.TypePassword, hashA, credential.ErrIdentityInvalid},
		{"unknown type", id, realmID, identityID, credential.Type("totp"), hashA, credential.ErrTypeInvalid},
		{"empty type", id, realmID, identityID, credential.Type(""), hashA, credential.ErrTypeInvalid},
		{"empty hash", id, realmID, identityID, credential.TypePassword, "", credential.ErrHashInvalid},
		{"plaintext hash", id, realmID, identityID, credential.TypePassword, "hunter2", credential.ErrHashInvalid},
	}

	for _, tc := range cases {
		_, err := credential.Rehydrate(tc.id, tc.realmID, tc.identityID, tc.typ, tc.hash, false, nil, now, now)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestReplaceChangesTheHashAndDecidesMustChange(t *testing.T) {
	for _, tc := range []struct{ before, after bool }{{true, false}, {false, true}, {true, true}} {
		c := newPassword(t, tc.before)
		created := c.CreatedAt()

		time.Sleep(time.Millisecond)

		if err := c.Replace(hashB, tc.after); err != nil {
			t.Fatalf("replacing: %v", err)
		}

		if c.Hash() != hashB {
			t.Errorf("hash: got %q", c.Hash())
		}

		if c.MustChange() != tc.after {
			t.Errorf("must change: want %t, got %t", tc.after, c.MustChange())
		}

		if !c.UpdatedAt().After(created) || !isMicrosecondUTC(c.UpdatedAt()) {
			t.Errorf("updated_at must move, in UTC at microsecond precision: got %v", c.UpdatedAt())
		}

		if !c.CreatedAt().Equal(created) {
			t.Error("created_at must not move")
		}
	}
}

func TestRehashChangesTheHashAndKeepsMustChange(t *testing.T) {
	for _, flag := range []bool{true, false} {
		c := newPassword(t, flag)
		before := c.UpdatedAt()

		time.Sleep(time.Millisecond)

		if err := c.Rehash(hashB); err != nil {
			t.Fatalf("rehashing: %v", err)
		}

		if c.Hash() != hashB {
			t.Errorf("hash: got %q", c.Hash())
		}

		if c.MustChange() != flag {
			t.Errorf("must change: want %t kept, got %t", flag, c.MustChange())
		}

		if !c.UpdatedAt().After(before) || !isMicrosecondUTC(c.UpdatedAt()) {
			t.Errorf("updated_at must move, in UTC at microsecond precision: got %v", c.UpdatedAt())
		}
	}
}

func TestARefusedHashLeavesTheCredentialUntouched(t *testing.T) {
	for name, hash := range malformedHashes {
		c := newPassword(t, true)
		before := c.UpdatedAt()

		if err := c.Replace(hash, false); !errors.Is(err, credential.ErrHashInvalid) {
			t.Errorf("replace, %s: want ErrHashInvalid, got %v", name, err)
		}

		if err := c.Rehash(hash); !errors.Is(err, credential.ErrHashInvalid) {
			t.Errorf("rehash, %s: want ErrHashInvalid, got %v", name, err)
		}

		if c.Hash() != hashA || !c.MustChange() || !c.UpdatedAt().Equal(before) {
			t.Errorf("%s: a refused hash must change nothing", name)
		}
	}
}

func TestMarkUsedStampsLastUseAndUpdatedAtTogether(t *testing.T) {
	c := newPassword(t, true)
	created := c.CreatedAt()

	time.Sleep(time.Millisecond)
	c.MarkUsed()

	used := c.LastUsedAt()
	if used == nil {
		t.Fatal("want a last use, got none")
	}

	if !used.Equal(c.UpdatedAt()) {
		t.Errorf("want one instant, got %v and %v", used, c.UpdatedAt())
	}

	if !used.After(created) || !isMicrosecondUTC(*used) {
		t.Errorf("last use must be now, in UTC at microsecond precision: got %v", used)
	}

	if c.Hash() != hashA || !c.MustChange() || !c.CreatedAt().Equal(created) {
		t.Error("a use must change nothing but the two timestamps")
	}
}

func TestLastUsedAtDoesNotExposeTheAggregate(t *testing.T) {
	c := newPassword(t, false)
	c.MarkUsed()

	want := *c.LastUsedAt()
	*c.LastUsedAt() = want.Add(24 * time.Hour)

	if got := c.LastUsedAt(); !got.Equal(want) {
		t.Errorf("writing through the returned pointer moved the last use to %v", got)
	}
}

func TestParseTypeRoundTrips(t *testing.T) {
	got, err := credential.ParseType(credential.TypePassword.String())
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	if got != credential.TypePassword {
		t.Errorf("want password, got %q", got)
	}

	for _, raw := range []string{"", "Password", "totp", " password"} {
		if _, err := credential.ParseType(raw); !errors.Is(err, credential.ErrTypeInvalid) {
			t.Errorf("%q: want ErrTypeInvalid, got %v", raw, err)
		}
	}
}
