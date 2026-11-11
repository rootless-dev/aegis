package identity_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/identity"
)

func emailOfLength(n int) string {
	const domain = "@example.com"

	return strings.Repeat("a", n-len(domain)) + domain
}

func newIdentity(t *testing.T) *identity.Identity {
	t.Helper()

	i, err := identity.New(uuid.Must(uuid.NewV7()), "ana@example.com", "ana")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	return i
}

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"already normal", "ana@example.com", "ana@example.com"},
		{"upper case", "Ana.Silva@Example.COM", "ana.silva@example.com"},
		{"surrounding space", "  ana@example.com\t\n", "ana@example.com"},
		{"plus tag", "ana+tag@example.com", "ana+tag@example.com"},
		{"single label domain", "ana@localhost", "ana@localhost"},
		{"at the limit", emailOfLength(254), emailOfLength(254)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.NormalizeEmail(tc.raw)
			if err != nil {
				t.Fatalf("%q must be accepted, got %v", tc.raw, err)
			}

			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestNormalizeEmailRejectsWhatIsNotABareAddress(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"only spaces":       "   ",
		"no at":             "ana.example.com",
		"no local part":     "@example.com",
		"no domain":         "ana@",
		"two ats":           "ana@b@example.com",
		"display name":      "Ana <ana@example.com>",
		"angle brackets":    "<ana@example.com>",
		"comment":           "ana@example.com (work)",
		"quoted local part": `"ana"@example.com`,
		"quoted at":         `"ana@b"@example.com`,
		"inner space":       "ana silva@example.com",
		"address list":      "ana@example.com, bob@example.com",
		"over the limit":    emailOfLength(255),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := identity.NormalizeEmail(raw); !errors.Is(err, identity.ErrEmailInvalid) {
				t.Errorf("%q must be refused with ErrEmailInvalid, got %v", raw, err)
			}
		})
	}
}

func TestNormalizeUsername(t *testing.T) {
	long := strings.Repeat("a", 64)

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty means none", "", ""},
		{"only spaces means none", "   ", ""},
		{"single character", "a", "a"},
		{"upper case", "Ana.Silva", "ana.silva"},
		{"surrounding space", "  ana \n", "ana"},
		{"inner separators", "a.b_c-d", "a.b_c-d"},
		{"digits", "007", "007"},
		{"at the limit", long, long},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.NormalizeUsername(tc.raw)
			if err != nil {
				t.Fatalf("%q must be accepted, got %v", tc.raw, err)
			}

			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestNormalizeUsernameRejectsMalformedValues(t *testing.T) {
	cases := map[string]string{
		// An "@" would let a username shadow somebody else's email at login.
		"looks like an email": "ana@example.com",
		"at inside":           "a@b",
		"leading separator":   ".ana",
		"trailing separator":  "ana-",
		"inner space":         "ana silva",
		"non ascii":           "joão",
		"slash":               "ana/silva",
		"over the limit":      strings.Repeat("a", 65),
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := identity.NormalizeUsername(raw); !errors.Is(err, identity.ErrUsernameInvalid) {
				t.Errorf("%q must be refused with ErrUsernameInvalid, got %v", raw, err)
			}
		})
	}
}

func TestNewNormalisesEmailAndUsername(t *testing.T) {
	realmID := uuid.Must(uuid.NewV7())

	i, err := identity.New(realmID, " Ana@Example.com ", " Ana.Silva ")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if i.Email() != "ana@example.com" {
		t.Errorf("email: got %q", i.Email())
	}

	if i.Username() != "ana.silva" || !i.HasUsername() {
		t.Errorf("username: got %q, has %v", i.Username(), i.HasUsername())
	}

	if i.RealmID() != realmID {
		t.Error("the realm must be the one given")
	}
}

func TestNewAcceptsAnIdentityWithoutUsername(t *testing.T) {
	i, err := identity.New(uuid.Must(uuid.NewV7()), "ana@example.com", "")
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if i.Username() != "" || i.HasUsername() {
		t.Errorf("want no username, got %q", i.Username())
	}
}

func TestNewGeneratesATimeOrderedIdentifier(t *testing.T) {
	first := newIdentity(t)
	second := newIdentity(t)

	if first.ID().Version() != 7 {
		t.Errorf("want a UUIDv7, got version %d", first.ID().Version())
	}

	// Keyset pagination orders by id and relies on that being creation order.
	if first.ID().String() >= second.ID().String() {
		t.Error("a later identity must sort after an earlier one")
	}
}

func TestNewStampsBothTimestampsInUTC(t *testing.T) {
	i := newIdentity(t)

	if i.CreatedAt().Location() != time.UTC || i.UpdatedAt().Location() != time.UTC {
		t.Error("timestamps must be UTC")
	}

	if !i.CreatedAt().Equal(i.UpdatedAt()) {
		t.Error("an identity that was never modified must carry the same instant in both")
	}

	if !i.CreatedAt().Equal(i.CreatedAt().Truncate(time.Microsecond)) {
		t.Error("timestamps must not carry more precision than the columns keep")
	}
}

func TestNewStartsActive(t *testing.T) {
	if i := newIdentity(t); i.Status() != identity.StatusActive {
		t.Errorf("want active, got %q", i.Status())
	}
}

func TestNewRejectsInvalidInput(t *testing.T) {
	realmID := uuid.Must(uuid.NewV7())

	cases := []struct {
		name     string
		realmID  uuid.UUID
		email    string
		username string
		want     error
	}{
		{"nil realm", uuid.Nil, "ana@example.com", "ana", identity.ErrRealmInvalid},
		{"empty email", realmID, "", "ana", identity.ErrEmailInvalid},
		{"display name", realmID, "Ana <ana@example.com>", "ana", identity.ErrEmailInvalid},
		{"email as username", realmID, "ana@example.com", "ana@example.com", identity.ErrUsernameInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := identity.New(tc.realmID, tc.email, tc.username); !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestRehydrateRoundTrips(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	realmID := uuid.Must(uuid.NewV7())
	created := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.FixedZone("BRT", -3*60*60))
	updated := created.Add(time.Hour)

	i, err := identity.Rehydrate(id, realmID, "ana@example.com", "ana", identity.StatusDisabled, created, updated)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if i.ID() != id || i.RealmID() != realmID {
		t.Error("the identifiers must be the stored ones")
	}

	if i.Email() != "ana@example.com" || i.Username() != "ana" {
		t.Errorf("email %q, username %q", i.Email(), i.Username())
	}

	if i.Status() != identity.StatusDisabled {
		t.Errorf("status: got %q", i.Status())
	}

	if !i.CreatedAt().Equal(created) || !i.UpdatedAt().Equal(updated) {
		t.Error("the instants must be the stored ones")
	}

	if i.CreatedAt().Location() != time.UTC || i.UpdatedAt().Location() != time.UTC {
		t.Error("timestamps must come back in UTC")
	}
}

func TestRehydrateAcceptsAnIdentityWithoutUsername(t *testing.T) {
	now := time.Now().UTC()

	i, err := identity.Rehydrate(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), "ana@example.com", "", identity.StatusActive, now, now)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if i.HasUsername() {
		t.Errorf("want no username, got %q", i.Username())
	}
}

// Data a customer's DBA has touched is not trusted input.
func TestRehydrateValidatesWhatItIsGiven(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	realmID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()

	cases := []struct {
		name     string
		id       uuid.UUID
		realmID  uuid.UUID
		email    string
		username string
		status   identity.Status
		want     error
	}{
		{"nil id", uuid.Nil, realmID, "ana@example.com", "ana", identity.StatusActive, identity.ErrIDInvalid},
		{"nil realm", id, uuid.Nil, "ana@example.com", "ana", identity.StatusActive, identity.ErrRealmInvalid},
		{"empty email", id, realmID, "", "ana", identity.StatusActive, identity.ErrEmailInvalid},
		{"malformed email", id, realmID, "ana.example.com", "ana", identity.StatusActive, identity.ErrEmailInvalid},
		{"email not lowercased", id, realmID, "Ana@example.com", "ana", identity.StatusActive, identity.ErrEmailInvalid},
		{"email not trimmed", id, realmID, " ana@example.com", "ana", identity.StatusActive, identity.ErrEmailInvalid},
		{"malformed username", id, realmID, "ana@example.com", "ana@example.com", identity.StatusActive, identity.ErrUsernameInvalid},
		{"username not lowercased", id, realmID, "ana@example.com", "Ana", identity.StatusActive, identity.ErrUsernameInvalid},
		{"username not trimmed", id, realmID, "ana@example.com", "ana ", identity.StatusActive, identity.ErrUsernameInvalid},
		{"blank username", id, realmID, "ana@example.com", " ", identity.StatusActive, identity.ErrUsernameInvalid},
		{"unknown status", id, realmID, "ana@example.com", "ana", identity.Status("deleted"), identity.ErrStatusInvalid},
		{"empty status", id, realmID, "ana@example.com", "ana", identity.Status(""), identity.ErrStatusInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := identity.Rehydrate(tc.id, tc.realmID, tc.email, tc.username, tc.status, now, now)
			if !errors.Is(err, tc.want) {
				t.Errorf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestSetStatusChangesTheStatusAndStampsUpdatedAt(t *testing.T) {
	i := newIdentity(t)

	created := i.CreatedAt()
	before := i.UpdatedAt()
	time.Sleep(time.Millisecond)

	if err := i.SetStatus(identity.StatusDisabled); err != nil {
		t.Fatalf("disabling: %v", err)
	}

	if i.Status() != identity.StatusDisabled {
		t.Errorf("status: got %q", i.Status())
	}

	if !i.UpdatedAt().After(before) {
		t.Error("updated_at must move")
	}

	if !i.CreatedAt().Equal(created) {
		t.Error("created_at must not move")
	}
}

func TestSetStatusRefusesAnUnknownValue(t *testing.T) {
	i := newIdentity(t)
	before := i.UpdatedAt()

	if err := i.SetStatus(identity.Status("deleted")); !errors.Is(err, identity.ErrStatusInvalid) {
		t.Errorf("want ErrStatusInvalid, got %v", err)
	}

	if i.Status() != identity.StatusActive || !i.UpdatedAt().Equal(before) {
		t.Error("a refused status must leave the identity untouched")
	}
}

func TestParseStatusRoundTripsBothValues(t *testing.T) {
	for _, want := range []identity.Status{identity.StatusActive, identity.StatusDisabled} {
		got, err := identity.ParseStatus(want.String())
		if err != nil {
			t.Errorf("%q: %v", want, err)
		}

		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	}

	if _, err := identity.ParseStatus("archived"); !errors.Is(err, identity.ErrStatusInvalid) {
		t.Errorf("want ErrStatusInvalid, got %v", err)
	}
}
