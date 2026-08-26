package database

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	pgmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	defaultPostgresPort = "5432"

	// 13: gen_random_uuid() without an extension, and the ON CONFLICT
	// behaviour the repositories will rely on.
	minimumPostgresVersion = 130000
)

// postgresReservedParams are the dsn keys this dialect sets itself and
// therefore strips from the caller's options. Lowercased because a GUC name is
// case-insensitive while url.Values keys are not: "timezone" and this
// dialect's own "TimeZone" would both reach pgx, and RuntimeParams is a map.
var postgresReservedParams = map[string]bool{
	"sslmode":         true,
	"sslrootcert":     true,
	"timezone":        true,
	"connect_timeout": true,
}

func postgresDSN(opts Options) (string, error) {
	params := url.Values{}

	for key, value := range opts.Options {
		if postgresReservedParams[strings.ToLower(key)] {
			continue
		}

		params.Set(key, value)
	}

	params.Set("sslmode", postgresSSLMode(opts.SSLMode))

	if opts.SSLRootCert != "" {
		params.Set("sslrootcert", opts.SSLRootCert)
	}

	// Expiry is compared across instances that may not share a timezone.
	params.Set("TimeZone", "UTC")

	if opts.ConnectTimeout > 0 {
		// Rounded up: libpq takes whole seconds, and a truncated sub-second
		// timeout becomes connect_timeout=0, which means no timeout at all.
		seconds := int(math.Ceil(opts.ConnectTimeout.Seconds()))
		params.Set("connect_timeout", strconv.Itoa(seconds))
	}

	port := opts.Port
	if port == "" {
		port = defaultPostgresPort
	}

	// Built as a url rather than concatenated: a customer's password routinely
	// contains characters that would be read as dsn syntax.
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(opts.User, opts.Password),
		Host:     net.JoinHostPort(opts.Host, port),
		Path:     "/" + opts.Name,
		RawQuery: params.Encode(),
	}

	return dsn.String(), nil
}

// postgresSSLMode is the identity translation, since the shared vocabulary is
// Postgres'. It exists so every dialect shows its mapping.
func postgresSSLMode(mode string) string {
	if mode == "" {
		return "prefer"
	}

	return mode
}

func postgresDialector(dsn string) gorm.Dialector {
	return postgres.Open(dsn)
}

func postgresMigrator(db *sql.DB) (migratedb.Driver, error) {
	return pgmigrate.WithInstance(db, &pgmigrate.Config{MigrationsTable: migrationsTable})
}

func postgresVersion(ctx context.Context, db *sql.DB) (string, error) {
	var version int

	if err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); err != nil {
		return "", fmt.Errorf("database: reading the postgres version: %w", err)
	}

	if version < minimumPostgresVersion {
		return "", fmt.Errorf(
			"database: postgres %d is below the minimum supported version %d",
			version, minimumPostgresVersion,
		)
	}

	// server_version_num is the comparable form; the readable one is what a
	// person reading a readiness report expects to see.
	return fmt.Sprintf("%d.%d", version/10000, version%10000), nil
}
