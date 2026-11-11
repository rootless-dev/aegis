//go:build integration

// The schema against a real server, selected by AEGIS_TEST_DRIVER. Everything here is raw SQL: these are the rules the schema
// holds on its own, whatever the domain and the repository above it refuse
// first.
package migrations_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
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

	db := openEmptyEngine(t)
	migrateThrough(t, db, latestVersion)

	return db
}

func openEmptyEngine(t *testing.T) *database.DB {
	t.Helper()

	db, err := database.Open(engineOptions(t))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}

	t.Cleanup(func() { _ = db.Shutdown(context.Background()) })

	return db
}

// migrateThrough stops at a version by handing the runner a copy of the real
// tree cut there, which is how a case gets rows in before a later migration.
func migrateThrough(t *testing.T, db *database.DB, version uint64) {
	t.Helper()

	tree, err := migrations.For(selectedDriver().String())
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}

	entries, err := fs.ReadDir(tree, ".")
	if err != nil {
		t.Fatalf("reading the migrations: %v", err)
	}

	cut := fstest.MapFS{}

	for _, entry := range entries {
		prefix, _, _ := strings.Cut(entry.Name(), "_")

		number, err := strconv.ParseUint(prefix, 10, 64)
		if err != nil {
			t.Fatalf("%s does not start with a version: %v", entry.Name(), err)
		}

		if number > version {
			continue
		}

		body, err := fs.ReadFile(tree, entry.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}

		cut[entry.Name()] = &fstest.MapFile{Data: body}
	}

	options := database.MigrateOptions{Timeout: time.Minute, LockTimeout: 30 * time.Second}

	if err := db.Migrate(context.Background(), cut, options); err != nil {
		t.Fatalf("migrating through %d: %v", version, err)
	}
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

// endedAt and endReason are any so a case can pass nil for NULL.
func insertSession(db *database.DB, id, realmID, identityID string, endedAt, endReason any) error {
	return db.Gorm.Exec(
		"INSERT INTO sessions (id, realm_id, identity_id, ip, user_agent, created_at, last_seen_at, expires_at, ended_at, end_reason)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		id, realmID, identityID, "2001:db8::1", "schema-test", now(), now(), now().Add(time.Hour), endedAt, endReason,
	).Error
}

func seedSession(t *testing.T, db *database.DB, realmID, identityID string) string {
	t.Helper()

	id := uuid.NewString()

	if err := insertSession(db, id, realmID, identityID, nil, nil); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	return id
}

func tokenHash() string {
	sum := sha256.Sum256([]byte(uuid.NewString()))

	return hex.EncodeToString(sum[:])
}

// parentID is any so a case can pass nil for NULL.
func insertRefreshToken(db *database.DB, id, realmID, sessionID string, parentID any, hash string) error {
	return db.Gorm.Exec(
		"INSERT INTO refresh_tokens (id, realm_id, session_id, parent_id, token_hash, created_at, expires_at, used_at)"+
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		id, realmID, sessionID, parentID, hash, now(), now().Add(time.Hour), nil,
	).Error
}

// seedTokenChain writes three tokens, each the child of the one before, and
// returns their ids oldest first.
func seedTokenChain(t *testing.T, db *database.DB, realmID, sessionID string) []string {
	t.Helper()

	ids := make([]string, 0, 3)

	var parentID any

	for range 3 {
		id := uuid.NewString()

		if err := insertRefreshToken(db, id, realmID, sessionID, parentID, tokenHash()); err != nil {
			t.Fatalf("seeding the token chain: %v", err)
		}

		ids = append(ids, id)
		parentID = id
	}

	return ids
}

// targetRealmID and marker are any so a case can pass nil for NULL.
func insertAdminGrant(db *database.DB, realmID, identityID string, targetRealmID, marker any) error {
	return db.Gorm.Exec(
		"INSERT INTO admin_grants (id, realm_id, identity_id, target_realm_id, global_marker, created_at)"+
			" VALUES (?, ?, ?, ?, ?, ?)",
		uuid.NewString(), realmID, identityID, targetRealmID, marker, now(),
	).Error
}

func seedGlobalGrant(t *testing.T, db *database.DB, realmID, identityID string) {
	t.Helper()

	if err := insertAdminGrant(db, realmID, identityID, nil, "y"); err != nil {
		t.Fatalf("seeding the global grant: %v", err)
	}
}

func seedRealmGrant(t *testing.T, db *database.DB, realmID, identityID, targetRealmID string) {
	t.Helper()

	if err := insertAdminGrant(db, realmID, identityID, targetRealmID, nil); err != nil {
		t.Fatalf("seeding the realm grant: %v", err)
	}
}

// SQLite keeps no catalogue of constraints, only the text that declared them.
func hasCheckConstraint(t *testing.T, db *database.DB, table, name string) bool {
	t.Helper()

	switch selectedDriver() {
	case database.DriverPostgres:
		return countRows(t, db,
			"SELECT COUNT(*) FROM pg_constraint WHERE contype = 'c' AND conrelid = to_regclass(?) AND conname = ?",
			table, name) == 1
	case database.DriverSQLite:
		return countRows(t, db,
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ? AND instr(sql, ?) > 0",
			table, "CONSTRAINT "+name+" CHECK") == 1
	default:
		return countRows(t, db,
			"SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS"+
				" WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = ? AND CONSTRAINT_TYPE = 'CHECK'",
			table, name) == 1
	}
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

const realmDefaults = "access_token_ttl = 300 AND session_idle_ttl = 1800 AND session_max_ttl = 36000"

// Its own engine, because two realms have to exist before 0012 runs: the
// defaults and 0016 only mean something against rows that were already there.
func TestSessionsAndTokensHoldTheirRules(t *testing.T) {
	db := openEmptyEngine(t)

	migrateThrough(t, db, 11)

	masterID := seedRealm(t, db, "master")
	existingID := seedRealm(t, db, "existing")

	migrateThrough(t, db, latestVersion)

	t.Run("a realm older than the settings is back-filled with the defaults", func(t *testing.T) {
		for _, id := range []string{masterID, existingID} {
			got := countRows(t, db, "SELECT COUNT(*) FROM realms WHERE id = ? AND "+realmDefaults, id)
			if got != 1 {
				t.Errorf("want realm %s back-filled with 300, 1800 and 36000, but it was not", id)
			}
		}
	})

	t.Run("only the master realm is given the password grant", func(t *testing.T) {
		createdAfterID := seedRealm(t, db, "created-after")

		if got := countRows(t, db, "SELECT COUNT(*) FROM realms WHERE id = ? AND password_grant = ?", masterID, true); got != 1 {
			t.Error("want the master realm to have password_grant true, but it does not")
		}

		for _, id := range []string{existingID, createdAfterID} {
			got := countRows(t, db,
				"SELECT COUNT(*) FROM realms WHERE id = ? AND password_grant = ? AND "+realmDefaults, id, false)
			if got != 1 {
				t.Errorf("want realm %s with the default TTLs and password_grant false, but it was not", id)
			}
		}
	})

	// A later migration can only drop what it can name, on every engine.
	t.Run("the checks added with the columns carry their names", func(t *testing.T) {
		names := []string{
			"ck_realms_access_token_ttl",
			"ck_realms_session_idle_ttl",
			"ck_realms_session_max_ttl",
		}

		// Postgres has a real boolean, so 0015 adds no check there.
		if selectedDriver() != database.DriverPostgres {
			names = append(names, "ck_realms_password_grant")
		}

		for _, name := range names {
			if !hasCheckConstraint(t, db, "realms", name) {
				t.Errorf("want a check constraint named %s on realms, found none", name)
			}
		}
	})

	t.Run("a TTL is a positive number of seconds", func(t *testing.T) {
		realmID := seedRealm(t, db, "ttl-check")

		for _, column := range []string{"access_token_ttl", "session_idle_ttl", "session_max_ttl"} {
			update := "UPDATE realms SET " + column + " = ? WHERE id = ?"

			if err := db.Gorm.Exec(update, 1, realmID).Error; err != nil {
				t.Fatalf("%s = 1 must be accepted: %v", column, err)
			}

			for _, value := range []int{0, -1} {
				if err := db.Gorm.Exec(update, value, realmID).Error; err == nil {
					t.Errorf("expected ck_realms_%s to refuse %d, but the update succeeded", column, value)
				}
			}
		}
	})

	t.Run("password_grant holds nothing but a boolean", func(t *testing.T) {
		realmID := seedRealm(t, db, "grant-check")
		update := "UPDATE realms SET password_grant = ? WHERE id = ?"

		for _, flag := range []bool{true, false} {
			if err := db.Gorm.Exec(update, flag, realmID).Error; err != nil {
				t.Fatalf("password_grant %v must be accepted: %v", flag, err)
			}
		}

		if err := db.Gorm.Exec(update, 2, realmID).Error; err == nil {
			t.Fatal("expected password_grant to refuse 2, but the update succeeded")
		}
	})

	t.Run("an unknown end reason is refused", func(t *testing.T) {
		realmID := seedRealm(t, db, "session-reason")
		identityID := seedIdentity(t, db, realmID, "one@example.com")

		for _, reason := range []string{"revoked", "reuse", "realm_disabled"} {
			if err := insertSession(db, uuid.NewString(), realmID, identityID, now(), reason); err != nil {
				t.Fatalf("end reason %q must be accepted: %v", reason, err)
			}
		}

		if err := insertSession(db, uuid.NewString(), realmID, identityID, now(), "expired"); err == nil {
			t.Fatal("expected ck_sessions_end_reason to refuse 'expired', but the insert succeeded")
		}
	})

	t.Run("a session ends with a reason or not at all", func(t *testing.T) {
		realmID := seedRealm(t, db, "session-ended")
		identityID := seedIdentity(t, db, realmID, "one@example.com")

		if err := insertSession(db, uuid.NewString(), realmID, identityID, nil, nil); err != nil {
			t.Fatalf("a live session must be accepted: %v", err)
		}

		if err := insertSession(db, uuid.NewString(), realmID, identityID, now(), "revoked"); err != nil {
			t.Fatalf("an ended session with its reason must be accepted: %v", err)
		}

		if err := insertSession(db, uuid.NewString(), realmID, identityID, now(), nil); err == nil {
			t.Error("expected ck_sessions_ended to refuse ended_at without end_reason, but the insert succeeded")
		}

		if err := insertSession(db, uuid.NewString(), realmID, identityID, nil, "revoked"); err == nil {
			t.Error("expected ck_sessions_ended to refuse end_reason without ended_at, but the insert succeeded")
		}
	})

	t.Run("a refresh token hash is unique across realms", func(t *testing.T) {
		first := seedRealm(t, db, "hash-first")
		second := seedRealm(t, db, "hash-second")
		firstSession := seedSession(t, db, first, seedIdentity(t, db, first, "one@example.com"))
		secondSession := seedSession(t, db, second, seedIdentity(t, db, second, "one@example.com"))
		hash := tokenHash()

		if err := insertRefreshToken(db, uuid.NewString(), first, firstSession, nil, hash); err != nil {
			t.Fatalf("seeding: %v", err)
		}

		if err := insertRefreshToken(db, uuid.NewString(), first, firstSession, nil, tokenHash()); err != nil {
			t.Fatalf("a second token with its own hash must be accepted: %v", err)
		}

		if err := insertRefreshToken(db, uuid.NewString(), second, secondSession, nil, hash); err == nil {
			t.Fatal("expected uq_refresh_tokens_hash to refuse a hash already stored, but the insert succeeded")
		}
	})

	// The rows a cascade removes also point at each other through parent_id,
	// hence a chain of three in every delete below.
	t.Run("deleting a realm removes its sessions and refresh tokens", func(t *testing.T) {
		realmID := seedRealm(t, db, "cascade-session-realm")
		keptRealmID := seedRealm(t, db, "cascade-session-realm-kept")

		for _, id := range []string{realmID, keptRealmID} {
			seedTokenChain(t, db, id, seedSession(t, db, id, seedIdentity(t, db, id, "one@example.com")))
		}

		if err := db.Gorm.Exec("DELETE FROM realms WHERE id = ?", realmID).Error; err != nil {
			t.Fatalf("deleting the realm: %v", err)
		}

		for table, kept := range map[string]int64{"sessions": 1, "refresh_tokens": 3} {
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+table+" WHERE realm_id = ?", realmID); got != 0 {
				t.Errorf("want the %s gone with the realm, got %d", table, got)
			}

			if got := countRows(t, db, "SELECT COUNT(*) FROM "+table+" WHERE realm_id = ?", keptRealmID); got != kept {
				t.Errorf("want the other realm to keep its %d rows in %s, got %d", kept, table, got)
			}
		}
	})

	t.Run("deleting an identity removes its sessions and refresh tokens", func(t *testing.T) {
		realmID := seedRealm(t, db, "cascade-session-identity")
		identityID := seedIdentity(t, db, realmID, "one@example.com")
		keptIdentityID := seedIdentity(t, db, realmID, "two@example.com")
		sessionID := seedSession(t, db, realmID, identityID)
		keptSessionID := seedSession(t, db, realmID, keptIdentityID)

		seedTokenChain(t, db, realmID, sessionID)
		seedTokenChain(t, db, realmID, keptSessionID)

		if err := db.Gorm.Exec("DELETE FROM identities WHERE id = ?", identityID).Error; err != nil {
			t.Fatalf("deleting the identity: %v", err)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM sessions WHERE identity_id = ?", identityID); got != 0 {
			t.Errorf("want the sessions gone with the identity, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM refresh_tokens WHERE session_id = ?", sessionID); got != 0 {
			t.Errorf("want the refresh tokens gone with the identity, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM refresh_tokens WHERE session_id = ?", keptSessionID); got != 3 {
			t.Errorf("want the other identity to keep its 3 refresh tokens, got %d", got)
		}
	})

	t.Run("deleting a session removes its refresh tokens", func(t *testing.T) {
		realmID := seedRealm(t, db, "cascade-session")
		identityID := seedIdentity(t, db, realmID, "one@example.com")
		sessionID := seedSession(t, db, realmID, identityID)
		keptSessionID := seedSession(t, db, realmID, identityID)

		seedTokenChain(t, db, realmID, sessionID)
		seedTokenChain(t, db, realmID, keptSessionID)

		if err := db.Gorm.Exec("DELETE FROM sessions WHERE id = ?", sessionID).Error; err != nil {
			t.Fatalf("deleting the session: %v", err)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM refresh_tokens WHERE session_id = ?", sessionID); got != 0 {
			t.Errorf("want the refresh tokens gone with the session, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM refresh_tokens WHERE session_id = ?", keptSessionID); got != 3 {
			t.Errorf("want the other session to keep its 3 refresh tokens, got %d", got)
		}
	})

	t.Run("deleting a parent token leaves its child without a parent", func(t *testing.T) {
		realmID := seedRealm(t, db, "token-parent")
		sessionID := seedSession(t, db, realmID, seedIdentity(t, db, realmID, "one@example.com"))
		chain := seedTokenChain(t, db, realmID, sessionID)

		if err := db.Gorm.Exec("DELETE FROM refresh_tokens WHERE id = ?", chain[0]).Error; err != nil {
			t.Fatalf("deleting the parent token: %v", err)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM refresh_tokens WHERE id = ? AND parent_id IS NULL", chain[1]); got != 1 {
			t.Errorf("want the child kept with parent_id NULL, got %d such rows", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM refresh_tokens WHERE id = ? AND parent_id = ?", chain[2], chain[1]); got != 1 {
			t.Errorf("want the grandchild still pointing at its own parent, got %d such rows", got)
		}
	})

	t.Run("a refresh token needs a parent that exists", func(t *testing.T) {
		realmID := seedRealm(t, db, "token-orphan")
		sessionID := seedSession(t, db, realmID, seedIdentity(t, db, realmID, "one@example.com"))

		if err := insertRefreshToken(db, uuid.NewString(), realmID, sessionID, uuid.NewString(), tokenHash()); err == nil {
			t.Fatal("expected fk_refresh_tokens_parent to refuse an unknown parent, but the insert succeeded")
		}
	})
}

func TestAdminGrantsHoldTheirRules(t *testing.T) {
	db := openEngine(t)

	t.Run("the checks carry their names", func(t *testing.T) {
		for _, name := range []string{"ck_admin_grants_scope", "ck_admin_grants_marker"} {
			if !hasCheckConstraint(t, db, "admin_grants", name) {
				t.Errorf("want a check constraint named %s on admin_grants, found none", name)
			}
		}
	})

	t.Run("an identity holds one global grant", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-global")
		identityID := seedIdentity(t, db, masterID, "one@example.com")

		if err := insertAdminGrant(db, masterID, identityID, nil, "y"); err != nil {
			t.Fatalf("a global grant must be accepted: %v", err)
		}

		if err := insertAdminGrant(db, masterID, identityID, nil, "y"); err == nil {
			t.Fatal("expected uq_admin_grants_global to refuse a second global grant, but the insert succeeded")
		}
	})

	t.Run("an identity holds one grant for a realm", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-target")
		targetID := seedRealm(t, db, "grant-target-first")
		otherTargetID := seedRealm(t, db, "grant-target-second")
		identityID := seedIdentity(t, db, masterID, "one@example.com")

		if err := insertAdminGrant(db, masterID, identityID, targetID, nil); err != nil {
			t.Fatalf("a realm grant must be accepted: %v", err)
		}

		if err := insertAdminGrant(db, masterID, identityID, otherTargetID, nil); err != nil {
			t.Fatalf("a grant for another realm must be accepted: %v", err)
		}

		if err := insertAdminGrant(db, masterID, identityID, targetID, nil); err == nil {
			t.Fatal("expected uq_admin_grants_target to refuse a second grant for the realm, but the insert succeeded")
		}
	})

	// Both unique indexes rest on NULLs not colliding: a global row is NULL in
	// one of them and a realm row is NULL in the other.
	t.Run("a global grant and realm grants coexist on one identity", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-mixed")
		identityID := seedIdentity(t, db, masterID, "one@example.com")

		seedGlobalGrant(t, db, masterID, identityID)
		seedRealmGrant(t, db, masterID, identityID, seedRealm(t, db, "grant-mixed-first"))
		seedRealmGrant(t, db, masterID, identityID, seedRealm(t, db, "grant-mixed-second"))

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE identity_id = ?", identityID); got != 3 {
			t.Errorf("want 3 grants on the identity, got %d", got)
		}
	})

	t.Run("two identities each hold a global grant", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-two-global")

		seedGlobalGrant(t, db, masterID, seedIdentity(t, db, masterID, "one@example.com"))

		if err := insertAdminGrant(db, masterID, seedIdentity(t, db, masterID, "two@example.com"), nil, "y"); err != nil {
			t.Fatalf("a global grant for another identity must be accepted: %v", err)
		}
	})

	t.Run("a grant is global or names a realm", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-scope")
		targetID := seedRealm(t, db, "grant-scope-target")

		seedGlobalGrant(t, db, masterID, seedIdentity(t, db, masterID, "global@example.com"))
		seedRealmGrant(t, db, masterID, seedIdentity(t, db, masterID, "realm@example.com"), targetID)

		if err := insertAdminGrant(db, masterID, seedIdentity(t, db, masterID, "both@example.com"), targetID, "y"); err == nil {
			t.Error("expected ck_admin_grants_scope to refuse a target with the marker, but the insert succeeded")
		}

		if err := insertAdminGrant(db, masterID, seedIdentity(t, db, masterID, "neither@example.com"), nil, nil); err == nil {
			t.Error("expected ck_admin_grants_scope to refuse a grant with neither, but the insert succeeded")
		}
	})

	t.Run("the marker holds nothing but 'y'", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-marker")

		seedGlobalGrant(t, db, masterID, seedIdentity(t, db, masterID, "one@example.com"))

		if err := insertAdminGrant(db, masterID, seedIdentity(t, db, masterID, "two@example.com"), nil, "n"); err == nil {
			t.Fatal("expected ck_admin_grants_marker to refuse 'n', but the insert succeeded")
		}
	})

	t.Run("a grant needs an identity and a target that exist", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-orphan")
		identityID := seedIdentity(t, db, masterID, "one@example.com")

		if err := insertAdminGrant(db, masterID, uuid.NewString(), nil, "y"); err == nil {
			t.Error("expected fk_admin_grants_identity to refuse an unknown identity, but the insert succeeded")
		}

		if err := insertAdminGrant(db, masterID, identityID, uuid.NewString(), nil); err == nil {
			t.Error("expected fk_admin_grants_target to refuse an unknown realm, but the insert succeeded")
		}
	})

	t.Run("deleting an identity removes its grants", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-cascade-identity")
		targetID := seedRealm(t, db, "grant-cascade-identity-target")
		identityID := seedIdentity(t, db, masterID, "one@example.com")
		keptIdentityID := seedIdentity(t, db, masterID, "two@example.com")

		for _, id := range []string{identityID, keptIdentityID} {
			seedGlobalGrant(t, db, masterID, id)
			seedRealmGrant(t, db, masterID, id, targetID)
		}

		if err := db.Gorm.Exec("DELETE FROM identities WHERE id = ?", identityID).Error; err != nil {
			t.Fatalf("deleting the identity: %v", err)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE identity_id = ?", identityID); got != 0 {
			t.Errorf("want the grants gone with the identity, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE identity_id = ?", keptIdentityID); got != 2 {
			t.Errorf("want the other identity to keep its 2 grants, got %d", got)
		}
	})

	t.Run("deleting the realm of an identity removes its grants", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-cascade-realm")
		keptMasterID := seedRealm(t, db, "grant-cascade-realm-kept")
		targetID := seedRealm(t, db, "grant-cascade-realm-target")

		for _, id := range []string{masterID, keptMasterID} {
			identityID := seedIdentity(t, db, id, "one@example.com")

			seedGlobalGrant(t, db, id, identityID)
			seedRealmGrant(t, db, id, identityID, targetID)
		}

		if err := db.Gorm.Exec("DELETE FROM realms WHERE id = ?", masterID).Error; err != nil {
			t.Fatalf("deleting the realm: %v", err)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE realm_id = ?", masterID); got != 0 {
			t.Errorf("want the grants gone with the realm, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE realm_id = ?", keptMasterID); got != 2 {
			t.Errorf("want the other realm to keep its 2 grants, got %d", got)
		}
	})

	// The second path from realms to admin_grants: the identity and its realm
	// stay, and only the rows naming the deleted realm go.
	t.Run("deleting a target realm removes the grants that name it", func(t *testing.T) {
		masterID := seedRealm(t, db, "grant-cascade-target")
		targetID := seedRealm(t, db, "grant-cascade-target-gone")
		keptTargetID := seedRealm(t, db, "grant-cascade-target-kept")
		identityID := seedIdentity(t, db, masterID, "one@example.com")

		seedGlobalGrant(t, db, masterID, identityID)
		seedRealmGrant(t, db, masterID, identityID, targetID)
		seedRealmGrant(t, db, masterID, identityID, keptTargetID)

		if err := db.Gorm.Exec("DELETE FROM realms WHERE id = ?", targetID).Error; err != nil {
			t.Fatalf("deleting the target realm: %v", err)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE target_realm_id = ?", targetID); got != 0 {
			t.Errorf("want the grants naming the realm gone, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE identity_id = ? AND global_marker = 'y'", identityID); got != 1 {
			t.Errorf("want the global grant kept, got %d", got)
		}

		if got := countRows(t, db, "SELECT COUNT(*) FROM admin_grants WHERE identity_id = ? AND target_realm_id = ?", identityID, keptTargetID); got != 1 {
			t.Errorf("want the grant for the other realm kept, got %d", got)
		}
	})
}
