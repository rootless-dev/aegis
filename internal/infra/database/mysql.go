package database

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	mysqlmigrate "github.com/golang-migrate/migrate/v4/database/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	defaultMySQLPort = "3306"

	// The floors: CTEs and window functions on MySQL 8, and stable utf8mb4
	// behaviour on MariaDB 10.6.
	minimumMySQLMajor   = 8
	minimumMariaDBMajor = 10
	minimumMariaDBMinor = 6
)

// mysqlReservedParams are the dsn keys this dialect sets itself and therefore
// strips from the caller's options. Lowercased because MySQL system variables
// are case-insensitive at the server: a supplied "TIME_ZONE" would otherwise
// land under its own map key and be sent as a second SET statement.
var mysqlReservedParams = map[string]bool{
	"parsetime":       true,
	"loc":             true,
	"multistatements": true,
	"tls":             true,
	"sql_mode":        true,
	"time_zone":       true,
	"charset":         true,
	"timeout":         true,
}

func mysqlDSN(opts Options) (string, error) {
	cfg := driver.NewConfig()

	cfg.User = opts.User
	cfg.Passwd = opts.Password
	cfg.Net = "tcp"

	port := opts.Port
	if port == "" {
		port = defaultMySQLPort
	}

	cfg.Addr = net.JoinHostPort(opts.Host, port)
	cfg.DBName = opts.Name
	cfg.Timeout = opts.ConnectTimeout

	// Without this every DATETIME arrives as a byte slice; without UTC, expiry
	// is written in whatever timezone the customer's server runs.
	cfg.ParseTime = true
	cfg.Loc = time.UTC

	// Off deliberately: with no transactional DDL, a migration file carrying
	// two statements could apply half of itself. Refused, it fails instead.
	cfg.MultiStatements = false

	// FormatDSN writes cfg.Params after the struct fields above, so a supplied
	// key would sit later in the dsn and win once the driver re-parses it.
	cfg.Params = map[string]string{}
	for key, value := range opts.Options {
		if mysqlReservedParams[strings.ToLower(key)] {
			continue
		}

		cfg.Params[key] = value
	}

	// Quoted because the driver passes these through as session variables.
	cfg.Params["sql_mode"] = "'STRICT_TRANS_TABLES'"
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.Params["charset"] = "utf8mb4"

	tlsName, err := mysqlTLS(opts)
	if err != nil {
		return "", err
	}

	cfg.TLSConfig = tlsName

	return cfg.FormatDSN(), nil
}

// mysqlTLS translates the shared vocabulary. The two verifying levels are not
// expressible in a dsn and have to be registered from Go.
func mysqlTLS(opts Options) (string, error) {
	switch opts.SSLMode {
	case "", "prefer":
		return "preferred", nil
	case "disable":
		return "false", nil
	case "require":
		// Encrypted, unverified: stops a passive listener, not an active one.
		return "skip-verify", nil
	case "verify-ca", "verify-full":
		return registerMySQLVerification(opts)
	default:
		return "", fmt.Errorf("database: unsupported ssl mode %q", opts.SSLMode)
	}
}

func registerMySQLVerification(opts Options) (string, error) {
	cfg, err := mysqlTLSConfig(opts)
	if err != nil {
		return "", err
	}

	name := mysqlTLSConfigName(opts)
	if err := driver.RegisterTLSConfig(name, cfg); err != nil {
		return "", fmt.Errorf("database: registering the tls configuration: %w", err)
	}

	return name, nil
}

// mysqlTLSConfig is split out so tests can build the same *tls.Config without
// going through the driver's global registry.
func mysqlTLSConfig(opts Options) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if opts.SSLRootCert != "" {
		// Boot configuration, not request input.
		pem, err := os.ReadFile(opts.SSLRootCert) // #nosec G304
		if err != nil {
			return nil, fmt.Errorf("database: reading the certificate authority %q: %w", opts.SSLRootCert, err)
		}

		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("database: %q contains no usable certificate", opts.SSLRootCert)
		}

		cfg.RootCAs = roots
	}

	if opts.SSLMode == "verify-ca" {
		// The chain without the name is not expressible in the standard
		// handshake. VerifyConnection, unlike VerifyPeerCertificate, also runs
		// on a resumed session, where a skipped check would accept anything.
		cfg.InsecureSkipVerify = true // #nosec G402
		cfg.VerifyConnection = verifyChainIgnoringHostname(cfg.RootCAs)
	} else {
		cfg.ServerName = opts.Host
	}

	return cfg, nil
}

// mysqlTLSConfigName is unique per configuration, not per mode:
// RegisterTLSConfig overwrites silently and the driver resolves the name on
// every connect, so a second pool would otherwise change the roots an earlier
// one is already using. The digest is truncated because this is a map key.
func mysqlTLSConfigName(opts Options) string {
	digest := sha256.Sum256([]byte(opts.Host + "\x00" + opts.SSLRootCert))

	return fmt.Sprintf("aegis-%s-%s-%x", opts.Driver, opts.SSLMode, digest[:8])
}

func verifyChainIgnoringHostname(roots *x509.CertPool) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("database: the server presented no certificate")
		}

		intermediates := x509.NewCertPool()
		for _, cert := range state.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}

		// No DNSName: that is the difference between verify-ca and verify-full.
		_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{
			Roots:         roots,
			Intermediates: intermediates,
		})

		return err
	}
}

func mysqlDialector(dsn string) gorm.Dialector {
	return mysql.Open(dsn)
}

func mysqlMigrator(db *sql.DB) (migratedb.Driver, error) {
	return mysqlmigrate.WithInstance(db, &mysqlmigrate.Config{})
}

func mysqlVersion(ctx context.Context, db *sql.DB) (string, error) {
	raw, err := serverVersion(ctx, db)
	if err != nil {
		return "", err
	}

	return raw, checkMySQLVersion(raw)
}

// checkMySQLVersion is split out so the decision can be tested against a
// string rather than a live server.
func checkMySQLVersion(raw string) error {
	// Declaring mysql against MariaDB would apply the wrong migration lineage.
	if strings.Contains(strings.ToLower(raw), "mariadb") {
		return fmt.Errorf("database: driver is %q but the server reports %q, declare %q instead", DriverMySQL, raw, DriverMariaDB)
	}

	major, _, err := parseVersion(raw)
	if err != nil {
		return err
	}

	if major < minimumMySQLMajor {
		return fmt.Errorf("database: mysql %s is below the minimum supported version %d.0", raw, minimumMySQLMajor)
	}

	return nil
}

func mariadbVersion(ctx context.Context, db *sql.DB) (string, error) {
	raw, err := serverVersion(ctx, db)
	if err != nil {
		return "", err
	}

	return raw, checkMariaDBVersion(raw)
}

// checkMariaDBVersion is split out for the same reason as checkMySQLVersion.
func checkMariaDBVersion(raw string) error {
	if !strings.Contains(strings.ToLower(raw), "mariadb") {
		return fmt.Errorf("database: driver is %q but the server reports %q, declare %q instead", DriverMariaDB, raw, DriverMySQL)
	}

	major, minor, err := parseVersion(raw)
	if err != nil {
		return err
	}

	if major < minimumMariaDBMajor || (major == minimumMariaDBMajor && minor < minimumMariaDBMinor) {
		return fmt.Errorf(
			"database: mariadb %s is below the minimum supported version %d.%d",
			raw, minimumMariaDBMajor, minimumMariaDBMinor,
		)
	}

	return nil
}

func serverVersion(ctx context.Context, db *sql.DB) (string, error) {
	var version string

	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return "", fmt.Errorf("database: reading the server version: %w", err)
	}

	return version, nil
}

// parseVersion reads the leading major.minor of strings like "8.0.36" and
// "10.6.16-MariaDB-1:10.6.16+maria~ubu2004". The "5.5.5-" some MariaDB builds
// prefix is a compatibility marker for old clients; left in place it reads as
// MySQL 5.5 and gets rejected as below the floor.
func parseVersion(raw string) (int, int, error) {
	fail := func() (int, int, error) {
		return 0, 0, fmt.Errorf("database: cannot read a version out of %q", raw)
	}

	trimmed := strings.TrimPrefix(raw, "5.5.5-")

	parts := strings.SplitN(trimmed, ".", 3)
	if len(parts) < 2 {
		return fail()
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return fail()
	}

	minor, err := strconv.Atoi(strings.SplitN(parts[1], "-", 2)[0])
	if err != nil {
		return fail()
	}

	return major, minor, nil
}
