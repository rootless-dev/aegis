package admin_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/admin"
)

func newID(t *testing.T) uuid.UUID {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generating an identifier: %v", err)
	}

	return id
}

func isMicrosecondUTC(at time.Time) bool {
	return at.Location() == time.UTC && at.Equal(at.Truncate(time.Microsecond))
}

func TestNewGlobalBuildsAGrantOverEveryRealm(t *testing.T) {
	realmID, identityID := newID(t), newID(t)

	g, err := admin.NewGlobal(realmID, identityID)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if g.ID().Version() != 7 {
		t.Errorf("want a UUIDv7, got version %d", g.ID().Version())
	}

	if g.RealmID() != realmID || g.IdentityID() != identityID {
		t.Error("the realm and the identity must be the ones given")
	}

	if !g.Global() || g.TargetRealmID() != uuid.Nil {
		t.Errorf("want a global grant with no target, got target %s", g.TargetRealmID())
	}

	if !isMicrosecondUTC(g.CreatedAt()) {
		t.Errorf("want UTC at microsecond precision, got %v", g.CreatedAt())
	}
}

func TestNewForRealmBuildsAGrantOverOneRealm(t *testing.T) {
	realmID, identityID, targetRealmID := newID(t), newID(t), newID(t)

	g, err := admin.NewForRealm(realmID, identityID, targetRealmID)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if g.ID().Version() != 7 {
		t.Errorf("want a UUIDv7, got version %d", g.ID().Version())
	}

	if g.RealmID() != realmID || g.IdentityID() != identityID {
		t.Error("the realm and the identity must be the ones given")
	}

	if g.Global() || g.TargetRealmID() != targetRealmID {
		t.Errorf("want a grant over %s, got target %s", targetRealmID, g.TargetRealmID())
	}

	if !isMicrosecondUTC(g.CreatedAt()) {
		t.Errorf("want UTC at microsecond precision, got %v", g.CreatedAt())
	}
}

func TestNewGlobalRejectsMissingOwners(t *testing.T) {
	cases := map[string]struct {
		realmID, identityID uuid.UUID
		want                error
	}{
		"no realm":    {uuid.Nil, newID(t), admin.ErrRealmInvalid},
		"no identity": {newID(t), uuid.Nil, admin.ErrIdentityInvalid},
	}

	for name, c := range cases {
		if _, err := admin.NewGlobal(c.realmID, c.identityID); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", name, c.want, err)
		}
	}
}

func TestNewForRealmRejectsMissingParts(t *testing.T) {
	cases := map[string]struct {
		realmID, identityID, targetRealmID uuid.UUID
		want                               error
	}{
		"no realm":    {uuid.Nil, newID(t), newID(t), admin.ErrRealmInvalid},
		"no identity": {newID(t), uuid.Nil, newID(t), admin.ErrIdentityInvalid},
		"no target":   {newID(t), newID(t), uuid.Nil, admin.ErrTargetInvalid},
	}

	for name, c := range cases {
		if _, err := admin.NewForRealm(c.realmID, c.identityID, c.targetRealmID); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", name, c.want, err)
		}
	}
}

func TestRehydrateRoundTrips(t *testing.T) {
	global, err := admin.NewGlobal(newID(t), newID(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	targeted, err := admin.NewForRealm(newID(t), newID(t), newID(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	for name, want := range map[string]*admin.Grant{"global": global, "targeted": targeted} {
		got, err := admin.Rehydrate(
			want.ID(), want.RealmID(), want.IdentityID(), want.TargetRealmID(), want.CreatedAt(),
		)
		if err != nil {
			t.Errorf("%s: rehydrating: %v", name, err)

			continue
		}

		if got.ID() != want.ID() || got.RealmID() != want.RealmID() || got.IdentityID() != want.IdentityID() {
			t.Errorf("%s: the identifiers must survive the round trip", name)
		}

		if got.TargetRealmID() != want.TargetRealmID() || got.Global() != want.Global() {
			t.Errorf("%s: want target %s, got %s", name, want.TargetRealmID(), got.TargetRealmID())
		}

		if !got.CreatedAt().Equal(want.CreatedAt()) {
			t.Errorf("%s: created at: want %v, got %v", name, want.CreatedAt(), got.CreatedAt())
		}
	}
}

func TestRehydrateNormalisesTheTimestampToUTC(t *testing.T) {
	stored := time.Date(2026, 3, 1, 9, 30, 0, 0, time.FixedZone("BRT", -3*60*60))

	g, err := admin.Rehydrate(newID(t), newID(t), newID(t), uuid.Nil, stored)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if g.CreatedAt().Location() != time.UTC || !g.CreatedAt().Equal(stored) {
		t.Errorf("want the same instant in UTC, got %v", g.CreatedAt())
	}
}

func TestRehydrateRejectsMissingParts(t *testing.T) {
	cases := map[string]struct {
		id, realmID, identityID uuid.UUID
		want                    error
	}{
		"no identifier": {uuid.Nil, newID(t), newID(t), admin.ErrIDInvalid},
		"no realm":      {newID(t), uuid.Nil, newID(t), admin.ErrRealmInvalid},
		"no identity":   {newID(t), newID(t), uuid.Nil, admin.ErrIdentityInvalid},
	}

	for name, c := range cases {
		if _, err := admin.Rehydrate(c.id, c.realmID, c.identityID, newID(t), time.Now()); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", name, c.want, err)
		}
	}
}

func TestCovers(t *testing.T) {
	target, other := newID(t), newID(t)

	global, err := admin.NewGlobal(newID(t), newID(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	targeted, err := admin.NewForRealm(newID(t), newID(t), target)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	cases := map[string]struct {
		grant   *admin.Grant
		realmID uuid.UUID
		want    bool
	}{
		"global, any realm":       {global, other, true},
		"global, the nil realm":   {global, uuid.Nil, false},
		"targeted, its realm":     {targeted, target, true},
		"targeted, another realm": {targeted, other, false},
		"targeted, the nil realm": {targeted, uuid.Nil, false},
		// The identity's own realm is not what the grant applies to.
		"targeted, the holder's realm": {targeted, targeted.RealmID(), false},
	}

	for name, c := range cases {
		if got := c.grant.Covers(c.realmID); got != c.want {
			t.Errorf("%s: want %t, got %t", name, c.want, got)
		}
	}
}
