package database

import (
	"database/sql"
	"net/url"
	"strings"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// sqliteForcedPragmas are stripped from the caller's options: SQLite runs
// every "_pragma" entry in query order, so a supplied one would win over the
// forced one below. Lowercased because a pragma name is case-insensitive.
var sqliteForcedPragmas = map[string]bool{
	"foreign_keys": true,
	"busy_timeout": true,
	"journal_mode": true,
}

func sqliteDSN(opts Options) (string, error) {
	params := url.Values{}

	// Off by default here, and enforced by the other three engines.
	params.Add("_pragma", "foreign_keys(1)")

	// Without it a concurrent write fails instead of waiting its turn.
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "journal_mode(WAL)")

	for key, value := range opts.Options {
		if key == "_pragma" && sqliteForcedPragmas[strings.ToLower(sqlitePragmaName(value))] {
			continue
		}

		params.Add(key, value)
	}

	return "file:" + escapeSQLitePath(opts.Path) + "?" + params.Encode(), nil
}

// escapeSQLitePath encodes the characters that would end the file name early
// once the driver parses the dsn as a uri. url.PathEscape would also escape
// the separators, which a path needs.
func escapeSQLitePath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
}

// sqlitePragmaName reads the pragma name out of a "_pragma" value such as
// "foreign_keys(1)" or "foreign_keys=1", ignoring whatever comes after it.
func sqlitePragmaName(pragma string) string {
	if idx := strings.IndexAny(pragma, "(= "); idx >= 0 {
		return pragma[:idx]
	}

	return pragma
}

func sqliteDialector(dsn string) gorm.Dialector {
	return gormlite.Open(dsn)
}

// sqliteMigrator goes through modernc.org/sqlite ("sqlite") while the
// dialector above goes through ncruces ("sqlite3"). Two implementations
// because glebarez, the obvious pairing, registers "sqlite" too and
// database/sql panics on the collision; gorm.io/driver/sqlite needs cgo.
func sqliteMigrator(db *sql.DB) (migratedb.Driver, error) {
	return sqlitemigrate.WithInstance(db, &sqlitemigrate.Config{MigrationsTable: migrationsTable})
}

// sqliteSingleWriter collapses the pool: SQLite serialises writes over the
// file, so more connections only turn contention into errors.
func sqliteSingleWriter(pool *Pool) {
	pool.MaxOpen = 1
	pool.MaxIdle = 1
}
