package database

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func postgresOptions() Options {
	return Options{
		Driver:         DriverPostgres,
		Host:           "db.internal",
		Name:           "aegis",
		User:           "aegis",
		Password:       "secret",
		ConnectTimeout: 10 * time.Second,
	}
}

func TestPostgresDSNForcesWhatTheCodeDependsOn(t *testing.T) {
	dsn, err := postgresDSN(postgresOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("the dsn is not a valid url: %v", err)
	}

	query := parsed.Query()

	if query.Get("TimeZone") != "UTC" {
		t.Fatalf("expected UTC to be forced, got %q", query.Get("TimeZone"))
	}

	if query.Get("connect_timeout") != "10" {
		t.Fatalf("expected the connect timeout in seconds, got %q", query.Get("connect_timeout"))
	}
}

// libpq only accepts whole seconds, and reads connect_timeout=0 as no timeout
// at all -- the exact opposite of what a sub-second value asked for.
func TestPostgresDSNRoundsUpASubSecondConnectTimeout(t *testing.T) {
	cases := map[time.Duration]string{
		500 * time.Millisecond:  "1",
		1500 * time.Millisecond: "2",
		2 * time.Second:         "2",
	}

	for timeout, wanted := range cases {
		t.Run(timeout.String(), func(t *testing.T) {
			opts := postgresOptions()
			opts.ConnectTimeout = timeout

			dsn, err := postgresDSN(opts)
			if err != nil {
				t.Fatalf("building the dsn: %v", err)
			}

			parsed, _ := url.Parse(dsn)
			if got := parsed.Query().Get("connect_timeout"); got != wanted {
				t.Fatalf("expected connect_timeout %q, got %q", wanted, got)
			}
		})
	}
}

func TestPostgresDSNDefaultsThePort(t *testing.T) {
	dsn, err := postgresDSN(postgresOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	if !strings.Contains(dsn, ":5432") {
		t.Fatalf("expected the engine default port, got %q", dsn)
	}
}

// A password is chosen by the customer and routinely contains characters that
// break a hand concatenated dsn.
func TestPostgresDSNEscapesTheCredentials(t *testing.T) {
	opts := postgresOptions()
	opts.Password = "p@ss/word:with?specials#"

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("the dsn is not a valid url: %v", err)
	}

	password, _ := parsed.User.Password()
	if password != opts.Password {
		t.Fatalf("expected the password to survive escaping, got %q", password)
	}

	if parsed.Host != "db.internal:5432" {
		t.Fatalf("expected the credentials not to bleed into the host, got %q", parsed.Host)
	}
}

func TestPostgresSSLModeTranslation(t *testing.T) {
	cases := map[string]string{
		"":            "prefer",
		"disable":     "disable",
		"prefer":      "prefer",
		"require":     "require",
		"verify-ca":   "verify-ca",
		"verify-full": "verify-full",
	}

	for given, wanted := range cases {
		t.Run("mode "+given, func(t *testing.T) {
			opts := postgresOptions()
			opts.SSLMode = given

			dsn, err := postgresDSN(opts)
			if err != nil {
				t.Fatalf("building the dsn: %v", err)
			}

			parsed, _ := url.Parse(dsn)
			if got := parsed.Query().Get("sslmode"); got != wanted {
				t.Fatalf("expected sslmode %q, got %q", wanted, got)
			}
		})
	}
}

func TestPostgresDSNCarriesTheCertificateAuthority(t *testing.T) {
	opts := postgresOptions()
	opts.SSLMode = "verify-full"
	opts.SSLRootCert = "/etc/aegis/db-ca.pem"

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := url.Parse(dsn)
	if got := parsed.Query().Get("sslrootcert"); got != opts.SSLRootCert {
		t.Fatalf("expected the ca file in the dsn, got %q", got)
	}
}

// Validation already rejects a colliding option, so this is the second line of
// defence: even if one arrives, the forced value has to win.
func TestPostgresForcedParametersOutrankOptions(t *testing.T) {
	opts := postgresOptions()
	opts.Options = map[string]string{"TimeZone": "America/Sao_Paulo", "application_name": "aegis"}

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := url.Parse(dsn)

	if got := parsed.Query().Get("TimeZone"); got != "UTC" {
		t.Fatalf("expected the forced timezone to win, got %q", got)
	}

	if got := parsed.Query().Get("application_name"); got != "aegis" {
		t.Fatalf("expected an unrelated option to survive, got %q", got)
	}
}

// Postgres GUC names are case-insensitive; url.Values keys are not. Without
// stripping opts.Options by its canonical, lowercased name, a caller-supplied
// "timezone" would sit next to this dialect's own "TimeZone" under a
// different query key, and pgx would carry both into RuntimeParams -- a Go
// map, so which one a session actually gets is iteration order, not this test.
func TestPostgresForcedParametersOutrankOptionsRegardlessOfCase(t *testing.T) {
	opts := postgresOptions()
	opts.Options = map[string]string{
		"timezone":         "America/Sao_Paulo",
		"SSLMODE":          "disable",
		"Connect_Timeout":  "999",
		"application_name": "aegis",
	}

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := url.Parse(dsn)
	query := parsed.Query()

	if _, ok := query["timezone"]; ok {
		t.Fatalf("expected the lowercase variant to be stripped rather than surviving alongside TimeZone, got %v", query)
	}

	if got := query.Get("TimeZone"); got != "UTC" {
		t.Fatalf("expected the forced timezone to win, got %q", got)
	}

	if _, ok := query["SSLMODE"]; ok {
		t.Fatalf("expected the differently cased sslmode to be stripped, got %v", query)
	}

	if got := query.Get("sslmode"); got != postgresSSLMode(opts.SSLMode) {
		t.Fatalf("expected the forced sslmode to win, got %q", got)
	}

	if _, ok := query["Connect_Timeout"]; ok {
		t.Fatalf("expected the differently cased connect_timeout to be stripped, got %v", query)
	}

	if got := query.Get("application_name"); got != "aegis" {
		t.Fatalf("expected an unrelated option to survive, got %q", got)
	}
}

// An operator with no ssl_root_cert configured must not be able to smuggle
// one in through Options under a different case -- sslrootcert is only Set
// from opts.SSLRootCert when that field is non-empty, so it has to be
// stripped from opts.Options unconditionally, not merely overwritten.
func TestPostgresDSNStripsAnOperatorSuppliedRootCertWhenNoneIsConfigured(t *testing.T) {
	opts := postgresOptions()
	opts.Options = map[string]string{"SSLRootCert": "/tmp/attacker-supplied.pem"}

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := url.Parse(dsn)
	query := parsed.Query()

	if got := query.Get("sslrootcert"); got != "" {
		t.Fatalf("expected no root cert to be set, got %q", got)
	}

	if _, ok := query["SSLRootCert"]; ok {
		t.Fatalf("expected the operator-supplied key to be stripped, got %v", query)
	}
}

func TestPostgresDSNForcesConnectTimeoutOverOptions(t *testing.T) {
	opts := postgresOptions()
	opts.ConnectTimeout = 7 * time.Second
	opts.Options = map[string]string{"connect_timeout": "999"}

	dsn, err := postgresDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := url.Parse(dsn)
	if got := parsed.Query().Get("connect_timeout"); got != "7" {
		t.Fatalf("expected the forced connect timeout to win, got %q", got)
	}
}

func TestTheDialectTableCoversEveryDriver(t *testing.T) {
	for _, driver := range []Driver{DriverPostgres, DriverMySQL, DriverMariaDB, DriverSQLite} {
		entry, ok := dialects()[driver]
		if !ok {
			t.Fatalf("driver %q has no dialect", driver)
		}

		if entry.dsn == nil || entry.dialector == nil || entry.migrator == nil {
			t.Fatalf("driver %q has an incomplete dialect", driver)
		}

		if entry.driverName == "" {
			t.Fatalf("driver %q has no registered database/sql driver name", driver)
		}
	}
}
