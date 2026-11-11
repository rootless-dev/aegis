//go:build integration

// The identities and credentials tables against a real server, selected by
// AEGIS_TEST_DRIVER. Everything here is raw SQL: these are the rules the schema
// holds on its own, whatever the domain and the repository above it refuse
// first.
package migrations_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/network"
	"github.com/rootless-dev/aegis/internal/infra/database"
	"github.com/rootless-dev/aegis/internal/migrations"
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

// openEngine migrates with the real embedded migrations, not a fixture.
func openEngine(t *testing.T) *database.DB {
	t.Helper()

	db, err := database.Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	tree, err := migrations.For(selectedDriver().String())
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}

	options := database.MigrateOptions{Timeout: time.Minute, LockTimeout: 30 * time.Second}

	if err := db.Migrate(context.Background(), tree, options); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	return db
}

func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// A slug per case: one engine serves the whole test, and uq_realms_slug does
// not care which case wrote the row.
func seedRealm(t *testing.T, db *database.DB, slug string) string {
	t.Helper()

	id := uuid.NewString()

	err := db.Gorm.Exec(
		"INSERT INTO realms (id, slug, display_name, issuer, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, slug, "Display", "https://"+slug+".example.com/realms/"+slug, "active", now(), now(),
	).Error
	if err != nil {
		t.Fatalf("seeding the realm %q: %v", slug, err)
	}

	return id
}

// username is any so a case can pass nil for NULL.
func insertIdentity(db *database.DB, id, realmID, email string, username any, status string) error {
	return db.Gorm.Exec(
		"INSERT INTO identities (id, realm_id, email, username, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		id, realmID, email, username, status, now(), now(),
	).Error
}

func seedIdentity(t *testing.T, db *database.DB, realmID, email string) string {
	t.Helper()

	id := uuid.NewString()

	if err := insertIdentity(db, id, realmID, email, nil, "active"); err != nil {
		t.Fatalf("seeding the identity %q: %v", email, err)
	}

	return id
}

// type and hash are written bare on purpose: both are keywords somewhere, and
// a dialect that needed them quoted would fail here before anything else.
// mustChange is any so a case can pass a value that is not a boolean.
func insertCredential(db *database.DB, realmID, identityID, kind string, mustChange any) error {
	return db.Gorm.Exec(
		"INSERT INTO credentials (id, realm_id, identity_id, type, hash, must_change, last_used_at, created_at, updated_at)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		uuid.NewString(), realmID, identityID, kind, "$argon2id$placeholder", mustChange, nil, now(), now(),
	).Error
}

func countRows(t *testing.T, db *database.DB, query string, args ...any) int64 {
	t.Helper()

	var count int64

	if err := db.Gorm.Raw(query, args...).Scan(&count).Error; err != nil {
		t.Fatalf("counting with %q: %v", query, err)
	}

	return count
}

// One test and one engine for every case: a container per case would spend
// minutes proving nothing more.
//
// Every refusal is paired with an accepted row of the same shape, so a case
// cannot pass because the insert was malformed for an unrelated reason.
func TestIdentitiesAndCredentialsHoldTheirRules(t *testing.T) {
	db := openEngine(t)

	t.Run("an unknown identity status is refused", func(t *testing.T) {
		realmID := seedRealm(t, db, "identity-status")

		for _, status := range []string{"active", "disabled"} {
			if err := insertIdentity(db, uuid.NewString(), realmID, status+"@example.com", nil, status); err != nil {
				t.Fatalf("status %q must be accepted: %v", status, err)
			}
		}

		if err := insertIdentity(db, uuid.NewString(), realmID, "deleted@example.com", nil, "deleted"); err == nil {
			t.Fatal("expected ck_identities_status to refuse 'deleted', but the insert succeeded")
		}
	})

	t.Run("an email is unique within its realm only", func(t *testing.T) {
		first := seedRealm(t, db, "email-first")
		second := seedRealm(t, db, "email-second")

		seedIdentity(t, db, first, "same@example.com")

		if err := insertIdentity(db, uuid.NewString(), second, "same@example.com", nil, "active"); err != nil {
			t.Fatalf("the same email in another realm must be accepted: %v", err)
		}

		if err := insertIdentity(db, uuid.NewString(), first, "same@example.com", nil, "active"); err == nil {
			t.Fatal("expected uq_identities_email to refuse a second identity with the email, but the insert succeeded")
		}
	})

	t.Run("a username is unique within its realm only", func(t *testing.T) {
		first := seedRealm(t, db, "username-first")
		second := seedRealm(t, db, "username-second")

		if err := insertIdentity(db, uuid.NewString(), first, "one@example.com", "taken", "active"); err != nil {
			t.Fatalf("seeding: %v", err)
		}

		if err := insertIdentity(db, uuid.NewString(), second, "one@example.com", "taken", "active"); err != nil {
			t.Fatalf("the same username in another realm must be accepted: %v", err)
		}

		if err := insertIdentity(db, uuid.NewString(), first, "two@example.com", "taken", "active"); err == nil {
			t.Fatal("expected uq_identities_username to refuse a second identity with the username, but the insert succeeded")
		}
	})

	// uq_identities_username rests on NULLs not colliding in a unique index,
	// which only a real engine can confirm.
	t.Run("many identities without a username share a realm", func(t *testing.T) {
		realmID := seedRealm(t, db, "username-null")

		seedIdentity(t, db, realmID, "one@example.com")
		seedIdentity(t, db, realmID, "two@example.com")

		got := countRows(t, db,
			"SELECT COUNT(*) FROM identities WHERE realm_id = ? AND username IS NULL", realmID)
		if got != 2 {
			t.Errorf("want 2 identities without a username, got %d", got)
		}
	})

	t.Run("an identity needs a realm that exists", func(t *testing.T) {
		if err := insertIdentity(db, uuid.NewString(), uuid.NewString(), "orphan@example.com", nil, "active"); err == nil {
			t.Fatal("expected fk_identities_realm to refuse an unknown realm, but the insert succeeded")
		}
	})

	t.Run("an unknown credential type is refused", func(t *testing.T) {
		realmID := seedRealm(t, db, "credential-type")

		if err := insertCredential(db, realmID, seedIdentity(t, db, realmID, "one@example.com"), "password", false); err != nil {
			t.Fatalf("type 'password' must be accepted: %v", err)
		}

		if err := insertCredential(db, realmID, seedIdentity(t, db, realmID, "two@example.com"), "totp", false); err == nil {
			t.Fatal("expected ck_credentials_type to refuse 'totp', but the insert succeeded")
		}
	})

	t.Run("an identity holds one credential of a type", func(t *testing.T) {
		realmID := seedRealm(t, db, "credential-twice")
		identityID := seedIdentity(t, db, realmID, "one@example.com")

		if err := insertCredential(db, realmID, identityID, "password", false); err != nil {
			t.Fatalf("seeding: %v", err)
		}

		if err := insertCredential(db, realmID, identityID, "password", true); err == nil {
			t.Fatal("expected uq_credentials_type to refuse a second password, but the insert succeeded")
		}
	})

	// Postgres refuses it by type; the other three have no boolean and rest on
	// ck_credentials_must_change.
	t.Run("must_change holds nothing but a boolean", func(t *testing.T) {
		realmID := seedRealm(t, db, "credential-flag")

		for _, flag := range []bool{false, true} {
			identityID := seedIdentity(t, db, realmID, uuid.NewString()+"@example.com")

			if err := insertCredential(db, realmID, identityID, "password", flag); err != nil {
				t.Fatalf("must_change %v must be accepted: %v", flag, err)
			}
		}

		if err := insertCredential(db, realmID, seedIdentity(t, db, realmID, "two@example.com"), "password", 2); err == nil {
			t.Fatal("expected must_change to refuse 2, but the insert succeeded")
		}
	})

	t.Run("a credential needs an identity that exists", func(t *testing.T) {
		realmID := seedRealm(t, db, "credential-orphan")

		if err := insertCredential(db, realmID, uuid.NewString(), "password", false); err == nil {
			t.Fatal("expected fk_credentials_identity to refuse an unknown identity, but the insert succeeded")
		}
	})

	t.Run("deleting a realm removes its identities and credentials", func(t *testing.T) {
		realmID := seedRealm(t, db, "cascade-realm")
		keptRealmID := seedRealm(t, db, "cascade-realm-kept")

		if err := insertCredential(db, realmID, seedIdentity(t, db, realmID, "one@example.com"), "password", false); err != nil {
			t.Fatalf("seeding: %v", err)
		}

		if err := insertCredential(db, keptRealmID, seedIdentity(t, db, keptRealmID, "one@example.com"), "password", false); err != nil {
			t.Fatalf("seeding the realm that stays: %v", err)
		}

		if err := db.Gorm.Exec("DELETE FROM realms WHERE id = ?", realmID).Error; err != nil {
			t.Fatalf("deleting the realm: %v", err)
		}

		for _, table := range []string{"identities", "credentials"} {
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+table+" WHERE realm_id = ?", realmID); got != 0 {
				t.Errorf("want the %s gone with the realm, got %d", table, got)
			}

			if got := countRows(t, db, "SELECT COUNT(*) FROM "+table+" WHERE realm_id = ?", keptRealmID); got != 1 {
				t.Errorf("want the other realm to keep its 1 row in %s, got %d", table, got)
			}
		}
	})

	t.Run("deleting an identity removes its credentials", func(t *testing.T) {
		realmID := seedRealm(t, db, "cascade-identity")
		identityID := seedIdentity(t, db, realmID, "one@example.com")
		keptIdentityID := seedIdentity(t, db, realmID, "two@example.com")

		for _, id := range []string{identityID, keptIdentityID} {
			if err := insertCredential(db, realmID, id, "password", false); err != nil {
				t.Fatalf("seeding: %v", err)
			}
		}

		if err := db.Gorm.Exec("DELETE FROM identities WHERE id = ?", identityID).Error; err != nil {
			t.Fatalf("deleting the identity: %v", err)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM credentials WHERE identity_id = ?", identityID); got != 0 {
			t.Errorf("want the credentials gone with the identity, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM credentials WHERE identity_id = ?", keptIdentityID); got != 1 {
			t.Errorf("want the other identity to keep its credential, got %d", got)
		}
	})
}
