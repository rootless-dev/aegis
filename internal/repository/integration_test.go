//go:build integration

// The repository against a real server, selected by AEGIS_TEST_DRIVER. Only
// what a real server can show; realm_test.go covers the rest against SQLite.
//
// makeRealm comes from realm_test.go, which carries no build constraint.
package repository_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/network"
	"github.com/rootless-dev/aegis/internal/domain/realm"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/migrations"
	"github.com/rootless-dev/aegis/internal/repository"
	"github.com/rootless-dev/aegis/internal/service"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Engine selection copied from internal/infra/database/containers_test.go: a
// _test.go file cannot be imported, so the smallest harness is duplicated here
// and kept identical in shape.

func selectedDriver() database.Driver {
	if driver := os.Getenv("AEGIS_TEST_DRIVER"); driver != "" {
		return database.Driver(driver)
	}

	return database.DriverSQLite
}

func engineOptions(t *testing.T) database.Options {
	t.Helper()

	base := database.Options{
		Driver:         selectedDriver(),
		Name:           "aegis",
		User:           "aegis",
		Password:       "aegis",
		SSLMode:        "disable",
		ConnectTimeout: 30 * time.Second,
		Pool: database.Pool{
			MaxOpen: 5, MaxIdle: 5, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute,
		},
	}

	switch base.Driver {
	case database.DriverSQLite:
		base.Name, base.User, base.Password, base.SSLMode = "", "", "", ""
		base.Path = filepath.Join(t.TempDir(), "aegis.db")
	case database.DriverPostgres:
		base.Host, base.Port = startPostgres(t)
	case database.DriverMySQL:
		base.Host, base.Port = startMySQL(t, "mysql:8.0")
	case database.DriverMariaDB:
		base.Host, base.Port = startMySQL(t, "mariadb:10.6")
	default:
		t.Fatalf("unknown test driver %q", base.Driver)
	}

	return base
}

func startPostgres(t *testing.T) (string, string) {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("aegis"),
		postgres.WithUsername("aegis"),
		postgres.WithPassword("aegis"),
		testcontainers.WithWaitStrategy(
			// Postgres restarts once during initialisation, so waiting for the
			// port alone connects to a server that is about to go away.
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	if err != nil {
		t.Fatalf("starting postgres: %v", err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	return endpoint(t, container, "5432/tcp")
}

// Serves both engines. The wait strategy is overridden because the module waits
// for a log line MariaDB never prints; WithOccurrence(2) skips the bootstrap
// server the entrypoint tears down.
func startMySQL(t *testing.T, image string) (string, string) {
	t.Helper()

	ctx := context.Background()

	opts := []testcontainers.ContainerCustomizer{
		mysql.WithDatabase("aegis"),
		mysql.WithUsername("aegis"),
		mysql.WithPassword("aegis"),
	}

	if strings.Contains(image, "mariadb") {
		opts = append(opts, testcontainers.WithWaitStrategy(
			wait.ForLog("mariadbd: ready for connections.").WithOccurrence(2),
		))
	}

	container, err := mysql.Run(ctx, image, opts...)
	if err != nil {
		t.Fatalf("starting %s: %v", image, err)
	}

	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	return endpoint(t, container, "3306/tcp")
}

// MappedPort re-inspects through the Docker API on every call, and that inspect
// races the port binding it asks about — hence the retry.
func endpoint(t *testing.T, container testcontainers.Container, port string) (string, string) {
	t.Helper()

	ctx := context.Background()

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("reading the container host: %v", err)
	}

	var mapped network.Port

	for attempt := 0; ; attempt++ {
		mapped, err = container.MappedPort(ctx, port)
		if err == nil {
			break
		}

		if attempt >= 4 {
			t.Fatalf("reading the mapped port: %v", err)
		}

		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}

	return host, mapped.Port()
}

// Store assembly
func migrateOptions() database.MigrateOptions {
	return database.MigrateOptions{Timeout: time.Minute, LockTimeout: 30 * time.Second}
}

// openEngine migrates with the real embedded migrations, not a fixture, and
// returns both the *database.DB for raw SQL and a Store on the same *gorm.DB.
func openEngine(t *testing.T) (*database.DB, service.Store) {
	t.Helper()

	db, err := database.Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	driver := selectedDriver().String()

	tree, err := migrations.For(driver)
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}

	if err := db.Migrate(context.Background(), tree, migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	return db, repository.NewStore(db.Gorm)
}

// insertRaw writes around the domain and the repository. The column-level
// assertions below need it: the domain refuses a 64-character slug or a
// "deleted" status long before either reaches the database.
func insertRaw(db *database.DB, id, slug, displayName, issuer, status string, at time.Time) error {
	return db.Gorm.Exec(
		"INSERT INTO realms (id, slug, display_name, issuer, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, slug, displayName, issuer, status, at, at,
	).Error
}

// Tests
func TestMigrationCreatesRealmsAtTheLatestVersion(t *testing.T) {
	db, _ := openEngine(t)

	expected, err := migrations.Latest(selectedDriver().String())
	if err != nil {
		t.Fatalf("reading the expected version: %v", err)
	}

	// Asserted against the recorded version, not merely that Migrate returned
	// no error: a missing dialect directory would also return no error, since
	// requireUpMigrations only inspects the source it was actually handed.
	version, dirty, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty || version != expected {
		t.Fatalf("expected a clean schema at version %d, got %d dirty=%v", expected, version, dirty)
	}
}

// This is the one assertion that catches an engine below the floor: MySQL
// parsed and silently discarded CHECK constraints before 8.0.16. A test that
// only looked for CHECK in the DDL would pass on a server that ignores it;
// this one requires the write to actually fail.
func TestCheckConstraintIsEnforcedNotMerelyDeclared(t *testing.T) {
	db, _ := openEngine(t)

	now := time.Now().UTC().Truncate(time.Microsecond)

	err := insertRaw(db,
		uuid.NewString(), "ck-status", "Display", "https://ck-status.example.com/realms/ck-status",
		"deleted", now,
	)
	if err == nil {
		t.Fatal("expected the CHECK constraint on status to reject 'deleted', but the insert succeeded")
	}
}

func TestSlugOver63CharsIsRefusedByTheColumn(t *testing.T) {
	if selectedDriver() == database.DriverSQLite {
		// SQLite's TEXT columns carry no length limit at all, STRICT tables
		// included: STRICT only checks type affinity, never width. The domain
		// is the only thing that ever enforces 63 characters on this engine.
		t.Skip("sqlite enforces no column length; the domain is the only bound here")
	}

	db, _ := openEngine(t)

	now := time.Now().UTC().Truncate(time.Microsecond)

	// slugPattern in the domain already caps a slug at 63 characters, so the
	// only way to reach the column's own limit is to write around the domain
	// entirely.
	tooLong := strings.Repeat("a", 64)

	err := insertRaw(db,
		uuid.NewString(), tooLong, "Display", "https://too-long.example.com/realms/x", "active", now,
	)
	if err == nil {
		t.Fatal("expected a 64-character slug to be refused by the column, but the insert succeeded")
	}
}

// The translation table in errors.go is keyed off constraint names for three
// engines and off the column name SQLite reports instead — the most
// engine-specific code in the slice, and realm_test.go only proves it against
// SQLite.
func TestCreateTranslatesTheUniqueViolationsOnTheSelectedEngine(t *testing.T) {
	_, store := openEngine(t)
	ctx := context.Background()

	first := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, first); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// A second realm from the same base URL would collide on slug and issuer
	// at once, and which one an engine reports first is not something to
	// depend on. A different base isolates the collision to the slug alone.
	otherBase, err := url.Parse("https://other.example.com")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	duplicateSlug, err := realm.New("acme", "Display acme", otherBase)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := store.Realms().Create(ctx, duplicateSlug); !errors.Is(err, realm.ErrSlugTaken) {
		t.Errorf("want realm.ErrSlugTaken, got %v", err)
	}
}

// The mirror of the case above, isolating the issuer collision the same way
// realm_test.go does: through Reissue onto an issuer another realm already
// holds, since two realms from the same base always share their slug too.
func TestReissueTranslatesTheIssuerViolationOnTheSelectedEngine(t *testing.T) {
	_, store := openEngine(t)
	ctx := context.Background()

	first := makeRealm(t, "acme")
	if err := store.Realms().Create(ctx, first); err != nil {
		t.Fatalf("creating: %v", err)
	}

	second := makeRealm(t, "beta")
	if err := store.Realms().Create(ctx, second); err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := store.Realms().Reissue(ctx, second.ID(), first.Issuer()); !errors.Is(err, realm.ErrIssuerTaken) {
		t.Errorf("want realm.ErrIssuerTaken, got %v", err)
	}
}

// Settles the one open design question: on Postgres the id column is `uuid` and
// the driver is pgx. Every other test here would already fail if a Go string
// did not bind without a cast; this one also requires the canonical
// 36-character form to survive the round trip unchanged.
func TestUUIDv7WrittenAsTextReadsBackIdentically(t *testing.T) {
	_, store := openEngine(t)
	ctx := context.Background()

	want := makeRealm(t, "uuid-roundtrip")
	if err := store.Realms().Create(ctx, want); err != nil {
		t.Fatalf("creating: %v", err)
	}

	got, err := store.Realms().FindByID(ctx, want.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if got.ID() != want.ID() {
		t.Errorf("id: want %s, got %s", want.ID(), got.ID())
	}

	if got.ID().String() != want.ID().String() {
		t.Errorf("id string form: want %q, got %q", want.ID().String(), got.ID().String())
	}

	if len(got.ID().String()) != 36 {
		t.Errorf("expected the canonical 36-character form, got %d characters: %q", len(got.ID().String()), got.ID().String())
	}
}

// timestamptz(6) and datetime(6) both carry microsecond precision; this is
// the assertion that a wider or narrower precision on the wire did not creep
// in silently, and that whatever the driver returned was converted to UTC.
func TestTimestampRoundTripsAtMicrosecondPrecisionInUTC(t *testing.T) {
	_, store := openEngine(t)
	ctx := context.Background()

	want := makeRealm(t, "timestamp-roundtrip")
	if err := store.Realms().Create(ctx, want); err != nil {
		t.Fatalf("creating: %v", err)
	}

	got, err := store.Realms().FindByID(ctx, want.ID())
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}

	if !got.CreatedAt().Equal(want.CreatedAt()) {
		t.Errorf("created_at: want %s, got %s", want.CreatedAt(), got.CreatedAt())
	}

	if got.CreatedAt().Nanosecond()%1000 != 0 {
		t.Errorf("expected no sub-microsecond noise, got %s", got.CreatedAt())
	}

	if got.CreatedAt().Location() != time.UTC {
		t.Errorf("expected the timestamp to come back in UTC, got location %s", got.CreatedAt().Location())
	}
}

func TestInTxRollsBackBothWritesOnFailure(t *testing.T) {
	_, store := openEngine(t)
	ctx := context.Background()

	sentinel := errors.New("stop")

	err := store.InTx(ctx, func(tx service.Store) error {
		if err := tx.Realms().Create(ctx, makeRealm(t, "first")); err != nil {
			return err
		}

		if err := tx.Realms().Create(ctx, makeRealm(t, "second")); err != nil {
			return err
		}

		return sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("the callback error must reach the caller, got %v", err)
	}

	for _, slug := range []string{"first", "second"} {
		if _, err := store.Realms().FindBySlug(ctx, slug); !errors.Is(err, realm.ErrNotFound) {
			t.Errorf("%q survived a rollback", slug)
		}
	}
}

func TestMigratingTwiceIsANoOp(t *testing.T) {
	db, _ := openEngine(t)

	driver := selectedDriver().String()

	expected, err := migrations.Latest(driver)
	if err != nil {
		t.Fatalf("reading the expected version: %v", err)
	}

	tree, err := migrations.For(driver)
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}

	if err := db.Migrate(context.Background(), tree, migrateOptions()); err != nil {
		t.Fatalf("migrating a second time: %v", err)
	}

	version, dirty, err := db.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("reading the schema version: %v", err)
	}

	if dirty || version != expected {
		t.Fatalf("expected the schema to stay at version %d, got %d dirty=%v", expected, version, dirty)
	}
}

// VerifySchema refuses a database that has not been migrated yet, and
// tolerates one recorded past what this binary carries — the rolling-update
// case, built here with ForceVersion since nothing in the running system ever
// produces a schema ahead on its own.
func TestVerifySchemaRefusesBehindAndToleratesAhead(t *testing.T) {
	opts := engineOptions(t)

	db, err := database.Open(opts)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	driver := selectedDriver().String()

	expected, err := migrations.Latest(driver)
	if err != nil {
		t.Fatalf("reading the expected version: %v", err)
	}

	ctx := context.Background()

	if err := db.VerifySchema(ctx, expected); !errors.Is(err, database.ErrSchemaBehind) {
		t.Errorf("want ErrSchemaBehind against an unmigrated database, got %v", err)
	}

	tree, err := migrations.For(driver)
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}

	if err := db.Migrate(ctx, tree, migrateOptions()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	if err := db.ForceVersion(ctx, int(expected)+1); err != nil {
		t.Fatalf("forcing a version ahead: %v", err)
	}

	if err := db.VerifySchema(ctx, expected); err != nil {
		t.Errorf("a schema ahead of this binary must be tolerated, got %v", err)
	}
}
