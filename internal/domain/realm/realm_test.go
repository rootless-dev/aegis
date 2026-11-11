package realm_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

func baseURL(t *testing.T) *url.URL {
	t.Helper()

	parsed, err := url.Parse("https://idp.example.com")
	if err != nil {
		t.Fatalf("parsing the base url: %v", err)
	}

	return parsed
}

func TestNewDerivesTheIssuerFromTheBaseURLAndSlug(t *testing.T) {
	r, err := realm.New("acme", "Acme Inc", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if want := "https://idp.example.com/realms/acme"; r.Issuer() != want {
		t.Errorf("issuer: want %q, got %q", want, r.Issuer())
	}

	// A trailing slash makes a different issuer as far as every client is
	// concerned: the comparison in the spec is byte for byte.
	if strings.HasSuffix(r.Issuer(), "/") {
		t.Error("the issuer must not end in a slash")
	}
}

func TestNewGeneratesATimeOrderedIdentifier(t *testing.T) {
	first, err := realm.New("acme", "Acme", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	second, err := realm.New("globex", "Globex", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if first.ID().Version() != 7 {
		t.Errorf("want a UUIDv7, got version %d", first.ID().Version())
	}

	// Keyset pagination orders by id and relies on that being creation order.
	if first.ID().String() >= second.ID().String() {
		t.Error("a later realm must sort after an earlier one")
	}
}

func TestNewStampsBothTimestampsInUTC(t *testing.T) {
	r, err := realm.New("acme", "Acme", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if r.CreatedAt().Location() != time.UTC || r.UpdatedAt().Location() != time.UTC {
		t.Error("timestamps must be UTC")
	}

	if !r.CreatedAt().Equal(r.UpdatedAt()) {
		t.Error("a realm that was never modified must carry the same instant in both")
	}
}

func TestNewStartsActive(t *testing.T) {
	r, err := realm.New("acme", "Acme", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if r.Status() != realm.StatusActive {
		t.Errorf("want active, got %q", r.Status())
	}
}

func TestNewRejectsMalformedSlugs(t *testing.T) {
	long := strings.Repeat("a", 64)

	for _, slug := range []string{"", "-acme", "acme-", "Acme", "ac me", "acme_1", ".well-known", "açme", long} {
		if _, err := realm.New(slug, "Acme", baseURL(t)); !errors.Is(err, realm.ErrSlugInvalid) {
			t.Errorf("slug %q must be refused with ErrSlugInvalid, got %v", slug, err)
		}
	}
}

func TestNewAcceptsSlugsAtBothBoundaries(t *testing.T) {
	for _, slug := range []string{"a", "a1", "a-b", strings.Repeat("a", 63)} {
		if _, err := realm.New(slug, "Acme", baseURL(t)); err != nil {
			t.Errorf("slug %q must be accepted, got %v", slug, err)
		}
	}
}

// The seed creates a realm called master, so a constructor that refused
// reserved slugs would refuse the one realm the system cannot start without.
// The reservation is a service rule; the aggregate only knows about form.
func TestNewAcceptsReservedSlugsBecauseTheRuleLivesInTheService(t *testing.T) {
	for _, slug := range []string{"master", "admin", "api", "aegis"} {
		if _, err := realm.New(slug, "Reserved", baseURL(t)); err != nil {
			t.Errorf("slug %q is well formed and must be accepted here, got %v", slug, err)
		}

		if !realm.IsReservedSlug(slug) {
			t.Errorf("%q must be reported as reserved", slug)
		}
	}

	if realm.IsReservedSlug("acme") {
		t.Error("acme is not reserved")
	}
}

func TestNewRejectsAnEmptyDisplayName(t *testing.T) {
	if _, err := realm.New("acme", "   ", baseURL(t)); !errors.Is(err, realm.ErrDisplayNameInvalid) {
		t.Errorf("want ErrDisplayNameInvalid, got %v", err)
	}
}

func TestNewRejectsABaseURLThatCannotProduceAValidIssuer(t *testing.T) {
	for _, raw := range []string{"https://idp.example.com/?a=b", "https://idp.example.com/#frag", "/relative"} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parsing %q: %v", raw, err)
		}

		if _, err := realm.New("acme", "Acme", parsed); !errors.Is(err, realm.ErrIssuerInvalid) {
			t.Errorf("base url %q must be refused, got %v", raw, err)
		}
	}

	if _, err := realm.New("acme", "Acme", nil); !errors.Is(err, realm.ErrIssuerInvalid) {
		t.Errorf("a nil base url must be refused, got %v", err)
	}
}

// Rehydrate is the repository's door back in. If it derived the issuer, every
// SELECT would recompute it from whatever public_url the process happens to
// hold — the silent drift the stored column exists to make impossible.
func TestRehydrateKeepsTheStoredIssuer(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	stored := "https://login.acme.com"
	now := time.Now().UTC().Truncate(time.Microsecond)

	settings := realm.Settings{
		AccessTokenTTL: 90 * time.Second,
		SessionIdleTTL: 10 * time.Minute,
		SessionMaxTTL:  time.Hour,
		PasswordGrant:  true,
	}

	r, err := realm.Rehydrate(id, "acme", "Acme", stored, realm.StatusActive, settings, now, now)
	if err != nil {
		t.Fatalf("rehydrating: %v", err)
	}

	if r.Settings() != settings {
		t.Errorf("settings: want %+v, got %+v", settings, r.Settings())
	}

	if r.Issuer() != stored {
		t.Errorf("issuer: want the stored %q, got %q", stored, r.Issuer())
	}

	if r.ID() != id {
		t.Error("the identifier must be the stored one")
	}
}

// Data a customer's DBA has touched is not trusted input.
func TestRehydrateValidatesWhatItIsGiven(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	settings := realm.DefaultSettings()

	if _, err := realm.Rehydrate(id, "Bad Slug", "Acme", "https://x", realm.StatusActive, settings, now, now); !errors.Is(err, realm.ErrSlugInvalid) {
		t.Errorf("a malformed stored slug must be refused, got %v", err)
	}

	if _, err := realm.Rehydrate(id, "acme", "Acme", "", realm.StatusActive, settings, now, now); !errors.Is(err, realm.ErrIssuerInvalid) {
		t.Errorf("an empty stored issuer must be refused, got %v", err)
	}

	if _, err := realm.Rehydrate(uuid.Nil, "acme", "Acme", "https://x", realm.StatusActive, settings, now, now); err == nil {
		t.Error("a nil identifier must be refused")
	}

	if _, err := realm.Rehydrate(id, "acme", "Acme", "https://x", realm.StatusActive, realm.Settings{}, now, now); !errors.Is(err, realm.ErrSettingsInvalid) {
		t.Errorf("zeroed stored settings must be refused, got %v", err)
	}
}

func TestNewStartsWithTheDefaultSettings(t *testing.T) {
	r, err := realm.New("acme", "Acme", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	want := realm.Settings{
		AccessTokenTTL: 5 * time.Minute,
		SessionIdleTTL: 30 * time.Minute,
		SessionMaxTTL:  10 * time.Hour,
		PasswordGrant:  false,
	}

	if r.Settings() != want || realm.DefaultSettings() != want {
		t.Errorf("want %+v, got %+v and %+v", want, r.Settings(), realm.DefaultSettings())
	}

	if err := realm.DefaultSettings().Validate(); err != nil {
		t.Errorf("the defaults must be valid, got %v", err)
	}
}

const tenYears = 10 * 365 * 24 * time.Hour

func TestSettingsValidateRejectsWhatTheColumnsCannotHold(t *testing.T) {
	cases := map[string]func(*realm.Settings){
		"zero access":       func(s *realm.Settings) { s.AccessTokenTTL = 0 },
		"zero idle":         func(s *realm.Settings) { s.SessionIdleTTL = 0 },
		"zero max":          func(s *realm.Settings) { s.SessionMaxTTL = 0 },
		"negative access":   func(s *realm.Settings) { s.AccessTokenTTL = -time.Second },
		"negative idle":     func(s *realm.Settings) { s.SessionIdleTTL = -time.Second },
		"negative max":      func(s *realm.Settings) { s.SessionMaxTTL = -time.Second },
		"fractional access": func(s *realm.Settings) { s.AccessTokenTTL = 1500 * time.Millisecond },
		"fractional idle":   func(s *realm.Settings) { s.SessionIdleTTL += time.Nanosecond },
		"fractional max":    func(s *realm.Settings) { s.SessionMaxTTL += time.Millisecond },
		"idle over max":     func(s *realm.Settings) { s.SessionIdleTTL = s.SessionMaxTTL + time.Second },
		"access over max":   func(s *realm.Settings) { s.AccessTokenTTL = s.SessionMaxTTL + time.Second },
		"max over the cap":  func(s *realm.Settings) { s.SessionMaxTTL = tenYears + time.Second },
	}

	for named, corrupt := range cases {
		t.Run(named, func(t *testing.T) {
			settings := realm.DefaultSettings()
			corrupt(&settings)

			if err := settings.Validate(); !errors.Is(err, realm.ErrSettingsInvalid) {
				t.Errorf("want ErrSettingsInvalid, got %v", err)
			}
		})
	}
}

func TestSettingsValidateAcceptsTheBoundaries(t *testing.T) {
	cases := map[string]realm.Settings{
		"one second each": {AccessTokenTTL: time.Second, SessionIdleTTL: time.Second, SessionMaxTTL: time.Second},
		"all at the cap":  {AccessTokenTTL: tenYears, SessionIdleTTL: tenYears, SessionMaxTTL: tenYears},
		"idle and access equal to max": {
			AccessTokenTTL: time.Hour, SessionIdleTTL: time.Hour, SessionMaxTTL: time.Hour, PasswordGrant: true,
		},
		// Nothing orders the access token against the idle window.
		"access over idle": {AccessTokenTTL: time.Hour, SessionIdleTTL: time.Minute, SessionMaxTTL: time.Hour},
	}

	for named, settings := range cases {
		t.Run(named, func(t *testing.T) {
			if err := settings.Validate(); err != nil {
				t.Errorf("want it accepted, got %v", err)
			}
		})
	}
}

func TestSetPasswordGrantFlipsTheFlagAndStampsUpdatedAt(t *testing.T) {
	r, err := realm.New("acme", "Acme", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	before := r.UpdatedAt()
	time.Sleep(time.Millisecond)

	r.SetPasswordGrant(true)

	if !r.Settings().PasswordGrant {
		t.Error("the grant must be on")
	}

	if !r.UpdatedAt().After(before) {
		t.Error("updated_at must move")
	}

	want := realm.DefaultSettings()
	want.PasswordGrant = true

	if r.Settings() != want {
		t.Errorf("the TTLs must be untouched, got %+v", r.Settings())
	}

	r.SetPasswordGrant(false)

	if r.Settings().PasswordGrant {
		t.Error("the grant must be off again")
	}
}

func TestRenameChangesTheNameAndStampsUpdatedAt(t *testing.T) {
	r, err := realm.New("acme", "Acme", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	before := r.UpdatedAt()
	time.Sleep(time.Millisecond)

	if err := r.Rename("Acme Corporation"); err != nil {
		t.Fatalf("renaming: %v", err)
	}

	if r.DisplayName() != "Acme Corporation" {
		t.Errorf("display name: got %q", r.DisplayName())
	}

	if !r.UpdatedAt().After(before) {
		t.Error("updated_at must move")
	}

	if r.CreatedAt().Equal(r.UpdatedAt()) {
		t.Error("created_at must not move")
	}
}

func TestSetStatusRefusesAnUnknownValue(t *testing.T) {
	r, err := realm.New("acme", "Acme", baseURL(t))
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := r.SetStatus(realm.Status("deleted")); !errors.Is(err, realm.ErrStatusInvalid) {
		t.Errorf("want ErrStatusInvalid, got %v", err)
	}

	if err := r.SetStatus(realm.StatusArchived); err != nil {
		t.Errorf("archived must be accepted, got %v", err)
	}
}

func TestParseStatusRoundTripsTheThreeValues(t *testing.T) {
	for _, want := range []realm.Status{realm.StatusActive, realm.StatusDisabled, realm.StatusArchived} {
		got, err := realm.ParseStatus(string(want))
		if err != nil {
			t.Errorf("%q: %v", want, err)
		}

		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	}

	if _, err := realm.ParseStatus("pending"); !errors.Is(err, realm.ErrStatusInvalid) {
		t.Errorf("want ErrStatusInvalid, got %v", err)
	}
}
