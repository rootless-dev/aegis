//go:build integration

// The administrator bootstrap, `aegisd bootstrap-admin` and `aegisd admin` end
// to end. Always Postgres, like the rest of this package.
package integration_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rootless-dev/aegis/internal/domain/token"
)

const (
	bootstrapEmail         = "root@example.com"
	bootstrapPassword      = "bootstrap-secret-one"
	bootstrapNextPassword  = "bootstrap-secret-two"
	bootstrapFinalPassword = "chosen-by-the-administrator"

	logCreated   = "first administrator created"
	logIgnored   = "the bootstrap credentials were ignored"
	logNone      = "no administrator able to sign in exists"
	logTemporary = "password is still the temporary one"
)

// adminFixture is one database booted on several times. It borrows the token
// fixture's requests and its record of secrets and outputs, and adds the log of
// every server it stopped, so the leak check covers all of them.
type adminFixture struct {
	*tokenFixture

	port string
	logs []string
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()

	port := freePort(t)
	env, endpoint := schemaEnv(t, port)

	f := &adminFixture{
		tokenFixture: &tokenFixture{
			env:      env,
			endpoint: endpoint,
			issuer:   "http://127.0.0.1:" + port + "/realms/master",
			secrets: []string{
				bootstrapPassword, bootstrapNextPassword, bootstrapFinalPassword, "$argon2id$",
			},
		},
		port: port,
	}

	t.Cleanup(func() {
		if f.server != nil {
			f.stop(t)
		}

		for _, log := range f.logs {
			f.assertNothingLeaked(t, "a server log", log)
		}

		for _, output := range f.outputs {
			f.assertNothingLeaked(t, "a command's output", output)
		}
	})

	return f
}

func withBootstrap(env []string, password string) []string {
	return append(append([]string{}, env...),
		"AEGIS_BOOTSTRAP_ADMIN_EMAIL="+bootstrapEmail,
		"AEGIS_BOOTSTRAP_ADMIN_PASSWORD="+password,
	)
}

func (f *adminFixture) boot(t *testing.T, env []string) {
	t.Helper()

	f.server = keyServer(t, f.port, env)
}

// stop returns the log, which is only complete once the process has exited.
func (f *adminFixture) stop(t *testing.T) string {
	t.Helper()

	stopBinary(t, f.server)

	log := f.server.output.String()

	f.logs = append(f.logs, log)
	f.server = nil

	return log
}

func (f *adminFixture) signIn(t *testing.T, password string) tokenResponse {
	t.Helper()

	resp, tokens, body := f.post(t, passwordForm(bootstrapEmail, password))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("password grant: want 200, got %d: %s", resp.StatusCode, body)
	}

	return tokens
}

func assertLog(t *testing.T, log string, present bool, fragments ...string) {
	t.Helper()

	for _, fragment := range fragments {
		if strings.Contains(log, fragment) != present {
			t.Errorf("want %q in the log = %v, got:\n%s", fragment, present, log)
		}
	}
}

func (e schemaEndpoint) identityIDOf(t *testing.T, email string) string {
	t.Helper()

	var id string

	err := e.open(t).SQL.QueryRowContext(context.Background(),
		`SELECT CAST(id AS text) FROM identities WHERE email = $1`, email).Scan(&id)
	if err != nil {
		t.Fatalf("reading the identity %s: %v", email, err)
	}

	return id
}

func (e schemaEndpoint) globalGrantsOf(t *testing.T, email string) int {
	t.Helper()

	var count int

	err := e.open(t).SQL.QueryRowContext(context.Background(),
		`SELECT count(*) FROM admin_grants g
		   JOIN identities i ON i.id = g.identity_id
		  WHERE i.email = $1 AND g.target_realm_id IS NULL`, email).Scan(&count)
	if err != nil {
		t.Fatalf("reading the grants of %s: %v", email, err)
	}

	return count
}

// No command creates a realm yet.
func (e schemaEndpoint) insertRealm(t *testing.T, slug string) {
	t.Helper()

	now := time.Now().UTC()

	_, err := e.open(t).SQL.ExecContext(context.Background(),
		`INSERT INTO realms (id, slug, display_name, issuer, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, 'active', $5, $6)`,
		uuid.Must(uuid.NewV7()).String(), slug, slug, "http://127.0.0.1/realms/"+slug, now, now)
	if err != nil {
		t.Fatalf("inserting the realm %s: %v", slug, err)
	}
}

// Phase 1 end to end: a boot with two variables, a sign in, and a token anyone
// holding the JWKS can verify.
func TestTheBootCreatesAnAdministratorWhoCanSignIn(t *testing.T) {
	f := newAdminFixture(t)

	f.boot(t, withBootstrap(f.env, bootstrapPassword))

	tokens := f.signIn(t, bootstrapPassword)

	status, jwks := f.server.get(t, jwksPath)
	if status != http.StatusOK {
		t.Fatalf("jwks: want 200, got %d: %s", status, jwks)
	}

	claims, err := token.Verify(tokens.AccessToken, jwksKeys(t, jwks), time.Now())
	if err != nil {
		t.Fatalf("verifying the access token against the jwks: %v", err)
	}

	if claims.Issuer != f.issuer {
		t.Errorf("iss = %q, want %q", claims.Issuer, f.issuer)
	}

	if want := f.endpoint.identityIDOf(t, bootstrapEmail); claims.Subject != want {
		t.Errorf("sub = %q, want the administrator %q", claims.Subject, want)
	}

	if _, mustChange, ok := f.endpoint.passwordOf(t, bootstrapEmail); !ok || !mustChange {
		t.Errorf("want a credential marked must_change, got ok=%v must_change=%v", ok, mustChange)
	}

	if got := f.endpoint.globalGrantsOf(t, bootstrapEmail); got != 1 {
		t.Errorf("want one global grant, got %d", got)
	}

	log := f.stop(t)
	assertLog(t, log, true, logCreated, logTemporary)
	assertLog(t, log, false, logIgnored, logNone)

	// A different password configured later is not a reset.
	f.boot(t, withBootstrap(f.env, bootstrapNextPassword))

	f.signIn(t, bootstrapPassword)
	f.assertRefused(t, "the password configured on the second boot", passwordForm(bootstrapEmail, bootstrapNextPassword))

	log = f.stop(t)
	assertLog(t, log, true, logIgnored, logTemporary)
	assertLog(t, log, false, logCreated, logNone)

	f.mustRunWith(t, withPasswordEnv(f.env, bootstrapFinalPassword), "identity", "set-password", "master", bootstrapEmail)

	f.boot(t, withBootstrap(f.env, bootstrapNextPassword))
	f.signIn(t, bootstrapFinalPassword)

	log = f.stop(t)
	assertLog(t, log, true, logIgnored)
	assertLog(t, log, false, logTemporary, logCreated, logNone)
}

func TestTheBootstrapPasswordIsReadFromAFile(t *testing.T) {
	f := newAdminFixture(t)

	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(bootstrapPassword+"\n"), 0o600); err != nil {
		t.Fatalf("writing the password file: %v", err)
	}

	f.boot(t, append(append([]string{}, f.env...),
		"AEGIS_BOOTSTRAP_ADMIN_EMAIL="+bootstrapEmail,
		"AEGIS_BOOTSTRAP_ADMIN_PASSWORD_FILE="+path,
	))

	f.signIn(t, bootstrapPassword)

	assertLog(t, f.stop(t), true, logCreated, logTemporary)
}

func TestBootstrapAdminCreatesThenResetsTheAdministrator(t *testing.T) {
	f := newAdminFixture(t)

	f.boot(t, f.env)

	log := f.stop(t)
	assertLog(t, log, true, logNone)
	assertLog(t, log, false, logCreated, logTemporary)

	code, output := f.command(t, f.env, "bootstrap-admin")
	if code != 1 {
		t.Fatalf("without the variables: want exit 1, got %d: %s", code, output)
	}

	for _, name := range []string{
		"AEGIS_BOOTSTRAP_ADMIN_EMAIL", "AEGIS_BOOTSTRAP_ADMIN_PASSWORD", "AEGIS_BOOTSTRAP_ADMIN_PASSWORD_FILE",
	} {
		if !strings.Contains(output, name) {
			t.Errorf("want the message to name %s, got %q", name, output)
		}
	}

	output = f.mustRunWith(t, withBootstrap(f.env, bootstrapPassword), "bootstrap-admin")

	for _, want := range []string{
		"administrator " + f.endpoint.identityIDOf(t, bootstrapEmail), bootstrapEmail,
		"global", "temporary", "session",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("want %q in the output, got:\n%s", want, output)
		}
	}

	if got := f.endpoint.globalGrantsOf(t, bootstrapEmail); got != 1 {
		t.Errorf("want one global grant, got %d", got)
	}

	f.boot(t, f.env)

	before := f.signIn(t, bootstrapPassword)

	f.mustRunWith(t, withBootstrap(f.env, bootstrapNextPassword), "bootstrap-admin")

	f.signIn(t, bootstrapNextPassword)
	f.assertRefused(t, "the password from before the reset", passwordForm(bootstrapEmail, bootstrapPassword))
	f.assertRefused(t, "a session opened before the reset", refreshForm(before.RefreshToken))

	if got := f.endpoint.globalGrantsOf(t, bootstrapEmail); got != 1 {
		t.Errorf("want the reset to leave one global grant, got %d", got)
	}

	log = f.stop(t)
	assertLog(t, log, true, logTemporary)
	assertLog(t, log, false, logNone)
}

// No server has ever booted on this database: the command seeds the master
// realm itself.
func TestBootstrapAdminNeedsNoServer(t *testing.T) {
	f := newAdminFixture(t)

	f.mustRun(t, "migrate")
	f.mustRunWith(t, withBootstrap(f.env, bootstrapPassword), "bootstrap-admin")

	f.boot(t, f.env)
	f.signIn(t, bootstrapPassword)

	assertLog(t, f.stop(t), false, logNone, logCreated)
}

func TestAdminGrantListAndRevoke(t *testing.T) {
	f := newAdminFixture(t)

	f.boot(t, withBootstrap(f.env, bootstrapPassword))
	f.stop(t)

	f.mustRun(t, "identity", "create", "master", "bia@example.com", "bia")
	f.endpoint.insertRealm(t, "acme")

	steps := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"grant", "bia"}, 0, "bia@example.com now administers every realm"},
		{[]string{"grant", "bia@example.com"}, 1, `aegisd: "bia@example.com" already administers every realm`},
		{[]string{"grant", "bia", "acme"}, 0, `bia@example.com now administers realm "acme"`},
		{[]string{"grant", "bia", "acme"}, 1, `aegisd: "bia" already administers realm "acme"`},
		{[]string{"grant", "ghost"}, 1, `aegisd: no identity "ghost" in realm "master"`},
		{[]string{"grant", "bia", "nope"}, 1, `aegisd: no realm named "nope"`},
		{[]string{"grant"}, 2, "aegisd: admin grant needs a login"},
	}

	for _, step := range steps {
		f.adminStep(t, step.args, step.code, step.want)
	}

	rows := f.adminRows(t)

	want := []string{
		bootstrapEmail + " - active global",
		"bia@example.com bia active global",
		"bia@example.com bia active acme",
	}

	if len(rows) != len(want) {
		t.Fatalf("want %d grants listed, got %v", len(want), rows)
	}

	for i, row := range rows {
		if !strings.HasPrefix(row, want[i]+" ") {
			t.Errorf("row %d: want %q followed by a date, got %q", i, want[i], row)
		}
	}

	steps = []struct {
		args []string
		code int
		want string
	}{
		{[]string{"revoke", "bia", "acme"}, 0, `bia@example.com no longer administers realm "acme"`},
		{[]string{"revoke", "bia", "acme"}, 1, `aegisd: "bia" does not administer realm "acme"`},
		{[]string{"revoke", "bia"}, 0, "bia@example.com no longer administers every realm"},
		{[]string{"revoke", "bia"}, 1, `aegisd: "bia" does not administer every realm`},
		{[]string{"revoke", "ghost@example.com"}, 1, `aegisd: no identity "ghost@example.com" in realm "master"`},
		{[]string{"revoke", "bia", "nope"}, 1, `aegisd: no realm named "nope"`},
		{[]string{"revoke"}, 2, "aegisd: admin revoke needs a login"},
	}

	for _, step := range steps {
		f.adminStep(t, step.args, step.code, step.want)
	}

	if rows := f.adminRows(t); len(rows) != 1 || !strings.HasPrefix(rows[0], bootstrapEmail) {
		t.Errorf("want only the bootstrapped administrator left, got %v", rows)
	}
}

func (f *adminFixture) adminStep(t *testing.T, args []string, wantCode int, want string) {
	t.Helper()

	code, output := f.command(t, f.env, append([]string{"admin"}, args...)...)
	if code != wantCode {
		t.Fatalf("admin %v: want exit %d, got %d: %s", args, wantCode, code, output)
	}

	if !strings.Contains(output, want) {
		t.Errorf("admin %v: want %q, got %q", args, want, output)
	}
}

// adminRows returns the table without its header, each row with its columns
// joined by one space.
func (f *adminFixture) adminRows(t *testing.T) []string {
	t.Helper()

	output := f.mustRun(t, "admin", "list")

	var rows []string

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)

		// The query log lands in the same stream.
		if strings.HasPrefix(line, "{") || len(fields) == 0 {
			continue
		}

		rows = append(rows, strings.Join(fields, " "))
	}

	if len(rows) == 0 || rows[0] != "EMAIL USERNAME STATUS SCOPE SINCE" {
		t.Fatalf("want the header first, got:\n%s", output)
	}

	return rows[1:]
}

// mustRunWith is mustRun under an environment that carries a secret.
func (f *adminFixture) mustRunWith(t *testing.T, env []string, args ...string) string {
	t.Helper()

	code, output := f.command(t, env, args...)
	if code != 0 {
		t.Fatalf("%v: want exit 0, got %d: %s", args, code, output)
	}

	return output
}
