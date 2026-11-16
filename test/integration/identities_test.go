//go:build integration

// `aegisd identity` end to end. Always Postgres, like the rest of this package.
package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	identityPassword     = "correct-horse-battery-staple"
	identityNextPassword = "an-entirely-different-secret"

	// The cost parameters are part of the assertion: a binary that hashed with
	// cheaper ones would still produce a valid argon2id string.
	argon2idPrefix = "$argon2id$v=19$m=65536,t=3,p=4$"
)

// identityEnv boots once and stops: the boot is the only thing that seeds the
// master realm, and no test here needs a server after that.
func identityEnv(t *testing.T) ([]string, schemaEndpoint) {
	t.Helper()

	port := freePort(t)
	env, endpoint := schemaEnv(t, port)

	server := keyServer(t, port, env)
	stopBinary(t, server)

	return env, endpoint
}

func withPasswordEnv(env []string, password string) []string {
	return append(append([]string{}, env...), "AEGIS_IDENTITY_PASSWORD="+password)
}

func identityCommand(t *testing.T, env []string, args ...string) string {
	t.Helper()

	code, output := runCommand(t, env, append([]string{"identity"}, args...)...)
	if code != 0 {
		t.Fatalf("identity %v: want exit 0, got %d: %s", args, code, output)
	}

	assertNoSecret(t, output)

	return output
}

func assertNoSecret(t *testing.T, output string) {
	t.Helper()

	for _, secret := range []string{identityPassword, identityNextPassword, "$argon2id$"} {
		if strings.Contains(output, secret) {
			t.Errorf("want no password or hash in the output, got %q in:\n%s", secret, output)
		}
	}
}

// ok is false when the identity has no credential.
func (e schemaEndpoint) passwordOf(t *testing.T, email string) (hash string, mustChange, ok bool) {
	t.Helper()

	rows, err := e.open(t).SQL.QueryContext(context.Background(),
		`SELECT c.hash, c.must_change FROM credentials c
		   JOIN identities i ON i.id = c.identity_id
		  WHERE i.email = $1 AND c.type = 'password'`, email)
	if err != nil {
		t.Fatalf("reading the credential of %s: %v", email, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			t.Fatalf("reading the credential of %s: %v", email, err)
		}

		return "", false, false
	}

	if err := rows.Scan(&hash, &mustChange); err != nil {
		t.Fatalf("scanning the credential of %s: %v", email, err)
	}

	return hash, mustChange, true
}

func (e schemaEndpoint) statusOf(t *testing.T, email string) string {
	t.Helper()

	var status string

	err := e.open(t).SQL.QueryRowContext(context.Background(),
		`SELECT status FROM identities WHERE email = $1`, email).Scan(&status)
	if err != nil {
		t.Fatalf("reading the status of %s: %v", email, err)
	}

	return status
}

func TestCreateWithAPasswordFromTheEnvironment(t *testing.T) {
	env, endpoint := identityEnv(t)

	output := identityCommand(t, withPasswordEnv(env, identityPassword), "create", "master", "Ana@Example.com", "ana")

	for _, want := range []string{"ana@example.com", "username: ana", "password: set"} {
		if !strings.Contains(output, want) {
			t.Errorf("want %q in the output, got:\n%s", want, output)
		}
	}

	if strings.Contains(output, "temporary") {
		t.Errorf("want no mention of a temporary password, got:\n%s", output)
	}

	hash, mustChange, ok := endpoint.passwordOf(t, "ana@example.com")
	if !ok {
		t.Fatal("want a stored credential, got none")
	}

	if !strings.HasPrefix(hash, argon2idPrefix) {
		t.Errorf("want the hash to start with %s, got %q", argon2idPrefix, hash)
	}

	if mustChange {
		t.Error("want must_change false without --temporary")
	}
}

func TestCreateWithATemporaryPasswordFromAFile(t *testing.T) {
	env, endpoint := identityEnv(t)

	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(identityPassword+"\n"), 0o600); err != nil {
		t.Fatalf("writing the password file: %v", err)
	}

	fileEnv := append(append([]string{}, env...), "AEGIS_IDENTITY_PASSWORD_FILE="+path)

	// Before the positional arguments, where it is hardest to get right.
	output := identityCommand(t, fileEnv, "create", "--temporary", "master", "bia@example.com")

	if !strings.Contains(output, "temporary") {
		t.Errorf("want the output to say the password is temporary, got:\n%s", output)
	}

	hash, mustChange, ok := endpoint.passwordOf(t, "bia@example.com")
	if !ok {
		t.Fatal("want a stored credential, got none")
	}

	if !strings.HasPrefix(hash, argon2idPrefix) {
		t.Errorf("want the hash to start with %s, got %q", argon2idPrefix, hash)
	}

	if !mustChange {
		t.Error("want must_change true with --temporary")
	}
}

func TestCreateWithoutAPasswordStoresNoCredential(t *testing.T) {
	env, endpoint := identityEnv(t)

	output := identityCommand(t, env, "create", "master", "caio@example.com")

	if !strings.Contains(output, "password: not set") {
		t.Errorf("want the output to say no password was set, got:\n%s", output)
	}

	if _, _, ok := endpoint.passwordOf(t, "caio@example.com"); ok {
		t.Error("want no credential, got one")
	}
}

func TestCreateRefusesADuplicateEmail(t *testing.T) {
	env, _ := identityEnv(t)
	withPassword := withPasswordEnv(env, identityPassword)

	identityCommand(t, withPassword, "create", "master", "ana@example.com")

	code, output := runCommand(t, withPassword, "identity", "create", "master", "ANA@example.com")
	if code != 1 {
		t.Fatalf("want exit 1, got %d: %s", code, output)
	}

	if !strings.Contains(output, `aegisd: the email "ANA@example.com" is already in use in realm "master"`) {
		t.Errorf("want the readable message, got %q", output)
	}

	assertNoSecret(t, output)
}

func TestListShowsEveryIdentity(t *testing.T) {
	env, _ := identityEnv(t)

	identityCommand(t, env, "create", "master", "ana@example.com", "ana")
	identityCommand(t, env, "create", "master", "bia@example.com")
	identityCommand(t, env, "disable", "master", "bia@example.com")

	output := identityCommand(t, env, "list", "master")

	// runCommand merges stderr in, and the query log lands there.
	var lines []string

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.HasPrefix(line, "{") {
			lines = append(lines, line)
		}
	}

	if len(lines) != 3 {
		t.Fatalf("want a header and two rows, got:\n%s", output)
	}

	if fields := strings.Fields(lines[0]); strings.Join(fields, " ") != "ID EMAIL USERNAME STATUS CREATED" {
		t.Errorf("want the header, got %q", lines[0])
	}

	// Ordered by id, and a UUIDv7 sorts by creation.
	if fields := strings.Fields(lines[1]); len(fields) < 4 || fields[1] != "ana@example.com" ||
		fields[2] != "ana" || fields[3] != "active" {
		t.Errorf("want ana, active, got %q", lines[1])
	}

	if fields := strings.Fields(lines[2]); len(fields) < 4 || fields[1] != "bia@example.com" ||
		fields[2] != "-" || fields[3] != "disabled" {
		t.Errorf("want bia without a username, disabled, got %q", lines[2])
	}
}

func TestSetPasswordGivesThenReplacesThePassword(t *testing.T) {
	env, endpoint := identityEnv(t)

	identityCommand(t, env, "create", "master", "ana@example.com", "ana")

	// By username, and with the flag after the arguments.
	output := identityCommand(t, withPasswordEnv(env, identityPassword), "set-password", "master", "ana", "--temporary")
	if !strings.Contains(output, "marked for replacement") {
		t.Errorf("want the output to say the password is temporary, got:\n%s", output)
	}

	first, mustChange, ok := endpoint.passwordOf(t, "ana@example.com")
	if !ok || !strings.HasPrefix(first, argon2idPrefix) {
		t.Fatalf("want an argon2id credential, got ok=%v hash=%q", ok, first)
	}

	if !mustChange {
		t.Error("want must_change true with --temporary")
	}

	identityCommand(t, withPasswordEnv(env, identityNextPassword), "set-password", "master", "ana@example.com")

	second, mustChange, ok := endpoint.passwordOf(t, "ana@example.com")
	if !ok || second == first {
		t.Errorf("want the hash replaced, got ok=%v unchanged=%v", ok, second == first)
	}

	if mustChange {
		t.Error("want must_change cleared by a replacement without --temporary")
	}
}

func TestSetPasswordWithoutAPasswordIsAUsageError(t *testing.T) {
	env, _ := identityEnv(t)

	code, output := runCommand(t, env, "identity", "set-password", "master", "ana@example.com")
	if code != 2 {
		t.Fatalf("want exit 2, got %d: %s", code, output)
	}

	for _, name := range []string{"AEGIS_IDENTITY_PASSWORD", "AEGIS_IDENTITY_PASSWORD_FILE"} {
		if !strings.Contains(output, name) {
			t.Errorf("want the message to name %s, got %q", name, output)
		}
	}
}

func TestDisableThenEnable(t *testing.T) {
	env, endpoint := identityEnv(t)

	identityCommand(t, env, "create", "master", "ana@example.com", "ana")

	identityCommand(t, env, "disable", "master", "ana")

	if got := endpoint.statusOf(t, "ana@example.com"); got != "disabled" {
		t.Errorf("want disabled, got %q", got)
	}

	identityCommand(t, env, "enable", "master", "ana@example.com")

	if got := endpoint.statusOf(t, "ana@example.com"); got != "active" {
		t.Errorf("want active, got %q", got)
	}
}

func TestIdentityVerbsNameWhatDoesNotExist(t *testing.T) {
	env, _ := identityEnv(t)
	withPassword := withPasswordEnv(env, identityPassword)

	cases := map[string]struct {
		args []string
		want string
	}{
		"create in an unknown realm": {
			args: []string{"create", "nope", "ana@example.com"}, want: `aegisd: no realm named "nope"`,
		},
		"list an unknown realm": {
			args: []string{"list", "nope"}, want: `aegisd: no realm named "nope"`,
		},
		"set-password on an unknown login": {
			args: []string{"set-password", "master", "ghost"}, want: `aegisd: no identity "ghost" in realm "master"`,
		},
		"disable an unknown login": {
			args: []string{"disable", "master", "ghost@example.com"},
			want: `aegisd: no identity "ghost@example.com" in realm "master"`,
		},
		"create with a malformed email": {
			args: []string{"create", "master", "not-an-email"}, want: `aegisd: "not-an-email" is not a well formed email`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, output := runCommand(t, withPassword, append([]string{"identity"}, tc.args...)...)
			if code != 1 {
				t.Fatalf("want exit 1, got %d: %s", code, output)
			}

			if !strings.Contains(output, tc.want) {
				t.Errorf("want %q, got %q", tc.want, output)
			}

			assertNoSecret(t, output)
		})
	}
}
