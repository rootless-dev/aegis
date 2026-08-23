// Package database is the connection factory for the four engines aegis
// supports: postgres, mysql, mariadb and sqlite. One file per dialect, and
// dialects() is the single table of what is supported.
package database

import (
	"context"
	"database/sql"

	migratedb "github.com/golang-migrate/migrate/v4/database"
	"gorm.io/gorm"
)

type Driver string

const (
	DriverPostgres Driver = "postgres"
	DriverMySQL    Driver = "mysql"
	DriverMariaDB  Driver = "mariadb"
	DriverSQLite   Driver = "sqlite"
)

func (d Driver) String() string {
	return string(d)
}

// dialect is everything that differs between engines; the rest is shared.
type dialect struct {
	dsn       func(Options) (string, error)
	dialector func(dsn string) gorm.Dialector

	// pool adjusts limits an engine cannot honor. Nil keeps them as configured.
	pool func(*Pool)

	// version refuses a server below the floor this code targets and reports
	// what it read, which the readiness detail then shows. Nil for an engine
	// that ships inside the binary.
	version func(context.Context, *sql.DB) (string, error)

	// migrator adapts an open pool to golang-migrate, whose config type differs
	// per engine.
	migrator func(*sql.DB) (migratedb.Driver, error)

	// driverName is what the migration runner opens its own connection as.
	driverName string
}

// dialects is spelled out rather than assembled through init(), like
// Application.sections() and Application.resources().
func dialects() map[Driver]dialect {
	return map[Driver]dialect{
		DriverPostgres: {
			dsn:        postgresDSN,
			dialector:  postgresDialector,
			version:    postgresVersion,
			migrator:   postgresMigrator,
			driverName: "postgres",
		},
		DriverMySQL: {
			dsn:        mysqlDSN,
			dialector:  mysqlDialector,
			version:    mysqlVersion,
			migrator:   mysqlMigrator,
			driverName: "mysql",
		},
		DriverMariaDB: {
			dsn:        mysqlDSN,
			dialector:  mysqlDialector,
			version:    mariadbVersion,
			migrator:   mysqlMigrator,
			driverName: "mysql",
		},
		DriverSQLite: {
			dsn:       sqliteDSN,
			dialector: sqliteDialector,
			pool:      sqliteSingleWriter,
			migrator:  sqliteMigrator,
			// Not what gormlite opens as; see sqliteMigrator.
			driverName: "sqlite",
		},
	}
}
