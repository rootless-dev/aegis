package database

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func mysqlOptions() Options {
	return Options{
		Driver:         DriverMySQL,
		Host:           "db.internal",
		Name:           "aegis",
		User:           "aegis",
		Password:       "secret",
		ConnectTimeout: 10 * time.Second,
	}
}

func TestMySQLDSNForcesWhatTheCodeDependsOn(t *testing.T) {
	dsn, err := mysqlDSN(mysqlOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("the dsn does not parse: %v", err)
	}

	if !parsed.ParseTime {
		t.Fatal("expected parseTime to be forced: without it every date column arrives as bytes")
	}

	if parsed.Loc != time.UTC {
		t.Fatalf("expected UTC to be forced, got %v", parsed.Loc)
	}

	// The one-statement-per-file rule leans on the driver refusing the second.
	if parsed.MultiStatements {
		t.Fatal("expected multiStatements to stay off")
	}

	if got := parsed.Params["sql_mode"]; got != "'STRICT_TRANS_TABLES'" {
		t.Fatalf("expected strict mode to be forced, got %q", got)
	}

	if got := parsed.Params["time_zone"]; got != "'+00:00'" {
		t.Fatalf("expected the session timezone to be forced, got %q", got)
	}

	if parsed.Params["charset"] != "utf8mb4" {
		t.Fatalf("expected utf8mb4 to be forced, got %q", parsed.Params["charset"])
	}
}

func TestMySQLDSNDefaultsThePort(t *testing.T) {
	dsn, err := mysqlDSN(mysqlOptions())
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := mysql.ParseDSN(dsn)
	if parsed.Addr != "db.internal:3306" {
		t.Fatalf("expected the engine default port, got %q", parsed.Addr)
	}
}

func TestMySQLDSNEscapesTheCredentials(t *testing.T) {
	opts := mysqlOptions()
	opts.Password = "p@ss/word:with?specials#"

	dsn, err := mysqlDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("the dsn does not parse: %v", err)
	}

	if parsed.Passwd != opts.Password {
		t.Fatalf("expected the password to survive escaping, got %q", parsed.Passwd)
	}
}

func TestMySQLSSLModeTranslation(t *testing.T) {
	cases := map[string]string{
		"":        "preferred",
		"disable": "false",
		"prefer":  "preferred",
		"require": "skip-verify",
	}

	for given, wanted := range cases {
		t.Run("mode "+given, func(t *testing.T) {
			opts := mysqlOptions()
			opts.SSLMode = given

			dsn, err := mysqlDSN(opts)
			if err != nil {
				t.Fatalf("building the dsn: %v", err)
			}

			parsed, _ := mysql.ParseDSN(dsn)
			if parsed.TLSConfig != wanted {
				t.Fatalf("expected tls %q, got %q", wanted, parsed.TLSConfig)
			}
		})
	}
}

// verify-ca and verify-full cannot be expressed as a dsn parameter at all: the
// driver only accepts the name of a config registered from Go.
func TestMySQLVerificationRegistersATLSConfig(t *testing.T) {
	opts := mysqlOptions()
	opts.SSLMode = "verify-full"

	dsn, err := mysqlDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, _ := mysql.ParseDSN(dsn)
	switch parsed.TLSConfig {
	case "", "preferred", "skip-verify", "false":
		t.Fatalf("expected a registered config name, got %q", parsed.TLSConfig)
	}
}

func TestMySQLRejectsAnUnreadableCertificateAuthority(t *testing.T) {
	opts := mysqlOptions()
	opts.SSLMode = "verify-full"
	opts.SSLRootCert = "/does/not/exist.pem"

	if _, err := mysqlDSN(opts); err == nil {
		t.Fatal("expected an unreadable ca file to fail the dsn")
	}
}

// Two pools opened under the same mode but a different host or a different
// SSLRootCert must not collide: RegisterTLSConfig silently overwrites an
// existing name, and the driver resolves it lazily on every connect, so a
// name that only encoded the mode would let the second registration
// retroactively change the first pool's roots and server name.
func TestMySQLTLSConfigNameIsUniquePerConfiguration(t *testing.T) {
	base := mysqlOptions()
	base.SSLMode = "verify-ca"

	differentHost := base
	differentHost.Host = "other.internal"

	if mysqlTLSConfigName(base) == mysqlTLSConfigName(differentHost) {
		t.Fatal("expected different hosts to produce different config names")
	}

	differentCA := base
	differentCA.SSLRootCert = "/etc/aegis/ca.pem"

	if mysqlTLSConfigName(base) == mysqlTLSConfigName(differentCA) {
		t.Fatal("expected different SSLRootCert paths to produce different config names")
	}

	// The other half of the rule: the name has to be stable across two
	// separately built Options that describe the same connection, or the second
	// pool would register a configuration the first one is not using.
	equivalent := Options{
		Driver:      base.Driver,
		SSLMode:     base.SSLMode,
		Host:        base.Host,
		SSLRootCert: base.SSLRootCert,
		User:        "another-user",
		Password:    "another-password",
	}

	if mysqlTLSConfigName(base) != mysqlTLSConfigName(equivalent) {
		t.Fatal("expected an equivalent tls configuration to produce the same name")
	}
}

// FormatDSN writes cfg.Params last, after the struct fields that mirror four
// of them (parseTime, loc, multiStatements, tls). Left unfiltered, an Options
// entry for one of those keys -- or for sql_mode/time_zone/charset -- would
// sit later in the dsn string than the forced value and win once re-parsed.
func TestMySQLDSNOptionsCannotShadowForcedFields(t *testing.T) {
	opts := mysqlOptions()
	opts.Options = map[string]string{
		"parseTime":         "false",
		"loc":               "Local",
		"multiStatements":   "true",
		"tls":               "false",
		"sql_mode":          "''",
		"time_zone":         "'SYSTEM'",
		"charset":           "latin1",
		"interpolateParams": "true",
	}

	dsn, err := mysqlDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("the dsn does not parse: %v", err)
	}

	if !parsed.ParseTime {
		t.Fatal("expected parseTime to stay forced")
	}

	if parsed.Loc != time.UTC {
		t.Fatalf("expected loc to stay forced to UTC, got %v", parsed.Loc)
	}

	if parsed.MultiStatements {
		t.Fatal("expected multiStatements to stay forced off")
	}

	if parsed.TLSConfig != "preferred" {
		t.Fatalf("expected tls to stay at the mode's own translation, got %q", parsed.TLSConfig)
	}

	if got := parsed.Params["sql_mode"]; got != "'STRICT_TRANS_TABLES'" {
		t.Fatalf("expected sql_mode to stay forced, got %q", got)
	}

	if got := parsed.Params["time_zone"]; got != "'+00:00'" {
		t.Fatalf("expected time_zone to stay forced, got %q", got)
	}

	if got := parsed.Params["charset"]; got != "utf8mb4" {
		t.Fatalf("expected charset to stay forced, got %q", got)
	}

	if !parsed.InterpolateParams {
		t.Fatal("expected an unrelated option to survive")
	}
}

// MySQL system variables (sql_mode, time_zone, charset) are case-insensitive
// at the server; a Go map key is not. Without comparing case-insensitively, an
// Options entry spelled "TIME_ZONE" would land in cfg.Params under a
// different key than this dialect's own "time_zone", and both would be sent
// as separate SET statements in whatever order cfg.Params -- a Go map --
// happens to range over. "Timeout" guards the same thing for the field
// FormatDSN writes as a plain dsn parameter rather than a session variable.
func TestMySQLDSNOptionsCannotShadowForcedFieldsRegardlessOfCase(t *testing.T) {
	opts := mysqlOptions()
	opts.Options = map[string]string{
		"TIME_ZONE": "'SYSTEM'",
		"SQL_MODE":  "''",
		"CHARSET":   "latin1",
		"ParseTime": "false",
		"Timeout":   "999s",
	}

	dsn, err := mysqlDSN(opts)
	if err != nil {
		t.Fatalf("building the dsn: %v", err)
	}

	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("the dsn does not parse: %v", err)
	}

	for _, key := range []string{"TIME_ZONE", "SQL_MODE", "CHARSET", "ParseTime", "Timeout"} {
		if _, ok := parsed.Params[key]; ok {
			t.Fatalf("expected the differently cased %q to be stripped rather than surviving alongside the forced field, got %v", key, parsed.Params)
		}
	}

	if got := parsed.Params["time_zone"]; got != "'+00:00'" {
		t.Fatalf("expected time_zone to stay forced, got %q", got)
	}

	if !parsed.ParseTime {
		t.Fatal("expected parseTime to stay forced")
	}

	if got := parsed.Timeout; got != opts.ConnectTimeout {
		t.Fatalf("expected the forced connect timeout to win, got %v", got)
	}
}

// generateTestCA and generateTestLeaf build a throwaway certificate hierarchy
// for the tests below, in the style of certs.GenerateSelfSigned but with the
// CA and the leaf as two distinct certificates: verifyChainIgnoringHostname
// has to be exercised against a leaf that a root actually signed, not a
// self-signed one.
func generateTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the ca key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aegis test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the ca certificate: %v", err)
	}

	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the ca certificate: %v", err)
	}

	return ca, key
}

func generateTestLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, hosts ...string) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the leaf key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "aegis test leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     hosts,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating the leaf certificate: %v", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the leaf certificate: %v", err)
	}

	return leaf
}

// writeTestCA writes a certificate as the pem file SSLRootCert expects, so the
// tests below exercise the exact same os.ReadFile path production takes.
func writeTestCA(t *testing.T, ca *x509.Certificate) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "ca.pem")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}

	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the test ca: %v", err)
	}

	return path
}

func TestVerifyCAAcceptsAMismatchedHostnameWithAValidChain(t *testing.T) {
	ca, caKey := generateTestCA(t)
	leaf := generateTestLeaf(t, ca, caKey, "db.internal")

	opts := mysqlOptions()
	opts.SSLMode = "verify-ca"
	opts.Host = "some-other-name.example.com" // deliberately not a name the leaf covers
	opts.SSLRootCert = writeTestCA(t, ca)

	cfg, err := mysqlTLSConfig(opts)
	if err != nil {
		t.Fatalf("building the tls config: %v", err)
	}

	if cfg.VerifyConnection == nil {
		t.Fatal("expected verify-ca to install a VerifyConnection callback")
	}

	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if err := cfg.VerifyConnection(state); err != nil {
		t.Fatalf("expected a valid chain under an unrelated hostname to be accepted, got %v", err)
	}
}

func TestVerifyCARejectsAnUnknownCA(t *testing.T) {
	ca, caKey := generateTestCA(t)
	leaf := generateTestLeaf(t, ca, caKey, "db.internal")

	unrelatedCA, _ := generateTestCA(t)

	opts := mysqlOptions()
	opts.SSLMode = "verify-ca"
	opts.SSLRootCert = writeTestCA(t, unrelatedCA)

	cfg, err := mysqlTLSConfig(opts)
	if err != nil {
		t.Fatalf("building the tls config: %v", err)
	}

	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if err := cfg.VerifyConnection(state); err == nil {
		t.Fatal("expected a certificate signed by an unknown ca to be rejected")
	}
}

func TestVerifyFullRejectsAMismatchedHostname(t *testing.T) {
	ca, caKey := generateTestCA(t)
	leaf := generateTestLeaf(t, ca, caKey, "db.internal")

	opts := mysqlOptions()
	opts.SSLMode = "verify-full"
	opts.Host = "attacker.example.com"
	opts.SSLRootCert = writeTestCA(t, ca)

	cfg, err := mysqlTLSConfig(opts)
	if err != nil {
		t.Fatalf("building the tls config: %v", err)
	}

	// verify-full installs no callback of its own: verification is the
	// standard library's default, which is exactly what crypto/tls runs when
	// InsecureSkipVerify is false and neither VerifyPeerCertificate nor
	// VerifyConnection is set -- the chain checked against Roots, the hostname
	// checked against DNSName, using the ServerName this dialect set to
	// opts.Host.
	if cfg.VerifyConnection != nil {
		t.Fatal("expected verify-full not to install a custom callback")
	}

	if _, err := leaf.Verify(x509.VerifyOptions{Roots: cfg.RootCAs, DNSName: cfg.ServerName}); err == nil {
		t.Fatal("expected a certificate not covering the configured hostname to be rejected")
	}
}

func TestVerifyFullAcceptsAMatchingHostname(t *testing.T) {
	ca, caKey := generateTestCA(t)
	leaf := generateTestLeaf(t, ca, caKey, "db.internal")

	opts := mysqlOptions()
	opts.SSLMode = "verify-full"
	opts.Host = "db.internal"
	opts.SSLRootCert = writeTestCA(t, ca)

	cfg, err := mysqlTLSConfig(opts)
	if err != nil {
		t.Fatalf("building the tls config: %v", err)
	}

	if _, err := leaf.Verify(x509.VerifyOptions{Roots: cfg.RootCAs, DNSName: cfg.ServerName}); err != nil {
		t.Fatalf("expected a certificate covering the configured hostname to be accepted, got %v", err)
	}
}

func TestVersionParsing(t *testing.T) {
	cases := map[string]struct {
		major int
		minor int
		patch int
	}{
		"8.0.36":                              {8, 0, 36},
		"10.6.16-MariaDB-1:10.6.16+maria~ubu": {10, 6, 16},
		"5.7.44":                              {5, 7, 44},
		// MariaDB prefixes some builds with "5.5.5-" for backward compatibility
		// with clients that only look at the first three tokens.
		"5.5.5-10.6.16-MariaDB-1:10.6.16+maria~ubu2004": {10, 6, 16},
	}

	for raw, wanted := range cases {
		t.Run(raw, func(t *testing.T) {
			major, minor, patch, err := parseVersion(raw)
			if err != nil {
				t.Fatalf("parsing %q: %v", raw, err)
			}

			if major != wanted.major || minor != wanted.minor || patch != wanted.patch {
				t.Fatalf("expected %d.%d.%d, got %d.%d.%d", wanted.major, wanted.minor, wanted.patch, major, minor, patch)
			}
		})
	}
}

func TestParseVersionRejectsAnUnparseableString(t *testing.T) {
	if _, _, _, err := parseVersion("not-a-version"); err == nil {
		t.Fatal("expected an unparseable string to fail")
	}
}

func TestCheckMySQLVersion(t *testing.T) {
	cases := map[string]bool{ // raw server version -> whether it should be rejected
		"7.9.99":                              true,  // below the floor
		"8.0.15":                              true,  // below the floor: CHECK is silently ignored
		"8.0.16":                              false, // exactly the floor
		"9.1.0":                               false, // above the floor
		"10.6.16-MariaDB-1:10.6.16+maria~ubu": true,  // mysql declared, mariadb reports back
		"not-a-version":                       true,  // unparseable
	}

	for raw, wantErr := range cases {
		t.Run(raw, func(t *testing.T) {
			err := checkMySQLVersion(raw)

			if wantErr && err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}

			if !wantErr && err != nil {
				t.Fatalf("expected %q to be accepted, got %v", raw, err)
			}
		})
	}
}

func TestCheckMariaDBVersion(t *testing.T) {
	cases := map[string]bool{ // raw server version -> whether it should be rejected
		"10.5.99-MariaDB":       true,  // below the floor
		"10.6.0-MariaDB":        false, // exactly the floor
		"10.7.0-MariaDB":        false, // above the floor
		"8.0.36":                true,  // mariadb declared, mysql reports back
		"not-a-version-MariaDB": true,  // mentions mariadb but has nothing to parse
	}

	for raw, wantErr := range cases {
		t.Run(raw, func(t *testing.T) {
			err := checkMariaDBVersion(raw)

			if wantErr && err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}

			if !wantErr && err != nil {
				t.Fatalf("expected %q to be accepted, got %v", raw, err)
			}
		})
	}
}

// MySQL parsed and then silently discarded CHECK constraints until 8.0.16: the
// CREATE TABLE succeeds, the constraint does not exist, and nothing reports it.
// The realms table relies on a CHECK for its status column, so 8.0 is no
// longer a floor aegis can stand on.
func TestMySQLFloorRequiresCheckConstraintSupport(t *testing.T) {
	below := []string{"8.0.0", "8.0.15", "5.7.44"}
	for _, raw := range below {
		if err := checkMySQLVersion(raw); err == nil {
			t.Errorf("%s is below 8.0.16 and must be refused", raw)
		}
	}

	accepted := []string{"8.0.16", "8.0.36", "8.4.0", "9.1.0"}
	for _, raw := range accepted {
		if err := checkMySQLVersion(raw); err != nil {
			t.Errorf("%s must be accepted, got %v", raw, err)
		}
	}
}

// A version with no patch component must not be read as patch zero and let
// 8.0 through the 8.0.16 floor by accident.
func TestMySQLVersionWithoutAPatchIsRefused(t *testing.T) {
	if err := checkMySQLVersion("8.0"); err == nil {
		t.Error("8.0 carries no patch level and cannot be shown to be at or above 8.0.16")
	}
}

func TestMariaDBFloorIsUnchanged(t *testing.T) {
	if err := checkMariaDBVersion("10.6.16-MariaDB-1:10.6.16+maria~ubu2004"); err != nil {
		t.Errorf("10.6 must still be accepted, got %v", err)
	}

	if err := checkMariaDBVersion("10.5.20-MariaDB"); err == nil {
		t.Error("10.5 must still be refused")
	}
}
