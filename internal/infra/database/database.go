package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
)

var ErrUnsupportedDriver = errors.New("database: unsupported driver")

type DB struct {
	Gorm *gorm.DB

	// SQL is here because GORM exposes neither Close nor Ping, which the
	// ordered shutdown and the readiness check need.
	SQL *sql.DB

	Driver Driver

	// version is what the server reported when the floor was checked at Open.
	// Empty for an engine that ships inside the binary and has no floor.
	version string

	// opts lets the migration runner open a connection of its own.
	opts Options
}

// Open connects before returning, so an unreachable database is an
// initialization error rather than a failure on the first request.
func Open(opts Options) (*DB, error) {
	selected, ok := dialects()[opts.Driver]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDriver, opts.Driver)
	}

	// Zero would expire the context below before the first packet, and the
	// failure would read as an unreachable server.
	if opts.ConnectTimeout <= 0 {
		return nil, errors.New("database: Options.ConnectTimeout must be positive")
	}

	pool := opts.Pool
	if selected.pool != nil {
		selected.pool(&pool)
	}

	dsn, err := selected.dsn(opts)
	if err != nil {
		return nil, err
	}

	gormDB, err := gorm.Open(selected.dialector(dsn), &gorm.Config{
		Logger: newGormLogger(opts.logger(), opts.SlowThreshold, !opts.LogParameters),
		// GORM otherwise wraps every write in a transaction of its own, a round
		// trip per statement; what needs atomicity will ask for it.
		SkipDefaultTransaction: true,
	})
	if err != nil {
		// gorm.Open pings unless told not to, so an unreachable server fails
		// here rather than at the explicit ping below.
		return nil, fmt.Errorf("database: %q is not reachable: %w", opts.Driver, err)
	}

	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("database: reaching the underlying pool: %w", err)
	}

	applyPool(sqlDB, pool)

	ctx, cancel := context.WithTimeout(context.Background(), opts.ConnectTimeout)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()

		return nil, fmt.Errorf("database: %q is not reachable: %w", opts.Driver, err)
	}

	var version string

	if selected.version != nil {
		version, err = selected.version(ctx, sqlDB)
		if err != nil {
			_ = sqlDB.Close()

			return nil, err
		}
	}

	announce(opts)

	return &DB{Gorm: gormDB, SQL: sqlDB, Driver: opts.Driver, version: version, opts: opts}, nil
}

// announce reports what was reached, once it has been reached: the ping and the
// version check above have already run, so this is a fact rather than the
// configuration that was aimed at. Never the password, and never the dsn that
// carries it.
func announce(opts Options) {
	entry := opts.logger().Info().Str("driver", opts.Driver.String())

	if opts.Driver == DriverSQLite {
		entry = entry.Str("path", opts.Path)
	} else {
		entry = entry.Str("host", opts.Host).Str("name", opts.Name)
	}

	entry.Msg("database connected")
}

func (db *DB) Ping(ctx context.Context) error {
	return db.SQL.PingContext(ctx)
}

// Probe is the readiness check. Alongside the answer it describes what is on
// the other end and how the pool is doing, which the health report shows only
// where it is safe to read. Everything here is either already held or comes
// from Stats, so a probe every few seconds costs one round trip and no query.
func (db *DB) Probe(ctx context.Context) (map[string]string, error) {
	started := time.Now()
	err := db.Ping(ctx)
	latency := time.Since(started)

	stats := db.SQL.Stats()

	details := map[string]string{
		"driver":          db.Driver.String(),
		"latency":         latency.Round(time.Microsecond).String(),
		"pool_open":       strconv.Itoa(stats.OpenConnections),
		"pool_in_use":     strconv.Itoa(stats.InUse),
		"pool_idle":       strconv.Itoa(stats.Idle),
		"pool_max_open":   strconv.Itoa(stats.MaxOpenConnections),
		"pool_wait_count": strconv.FormatInt(stats.WaitCount, 10),
	}

	if db.version != "" {
		details["version"] = db.version
	}

	if db.Driver == DriverSQLite {
		details["path"] = db.opts.Path
	} else {
		details["host"] = db.opts.Host
		details["name"] = db.opts.Name
	}

	return details, err
}

// Shutdown takes an unused context to match what graceful.Register expects.
// sql.DB.Close does not wait for in-flight queries, which is safe only because
// this runs after the server has drained.
func (db *DB) Shutdown(_ context.Context) error {
	return db.SQL.Close()
}
