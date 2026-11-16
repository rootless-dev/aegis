package application

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/configs"
)

const (
	adminEmail        = "root@example.com"
	adminPassword     = "bootstrap-secret-one"
	adminNextPassword = "bootstrap-secret-two"

	msgCreated   = "first administrator created"
	msgIgnored   = "the bootstrap credentials were ignored"
	msgNone      = "no administrator able to sign in exists"
	msgTemporary = "password is still the temporary one"
)

type logLine struct {
	Level   string `json:"level"`
	Email   string `json:"email"`
	Message string `json:"message"`
}

func bootConfig(t *testing.T) *configs.Application {
	t.Helper()

	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.Crypto.MasterKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg.Normalize()
	cfg.Database.Path = filepath.Join(t.TempDir(), "aegis.db")

	return cfg
}

// boot walks the steps up to setServices with the log captured, which New does
// not allow, and closes the pool so the same file can be booted on again. No
// boot may ever log a password, so that is asserted here for all of them.
func boot(t *testing.T, cfg *configs.Application) (*Application, []logLine) {
	t.Helper()

	var logs bytes.Buffer

	app := &Application{cfg: cfg, logger: &log.Logger{Writer: log.IOWriter{Writer: &logs}}}

	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	for _, step := range []func() error{
		app.setGraceful, app.setHealth, app.setDatabase, app.setSchema, app.setCrypto, app.setServices,
	} {
		if err := step(); err != nil {
			t.Fatalf("booting: %v", err)
		}
	}

	for _, secret := range []string{adminPassword, adminNextPassword} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("want no password in the log, got %q in:\n%s", secret, logs.String())
		}
	}

	var lines []logLine

	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var line logLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("decoding the log line %q: %v", raw, err)
		}

		lines = append(lines, line)
	}

	return app, lines
}

func reboot(t *testing.T, app *Application, cfg *configs.Application) (*Application, []logLine) {
	t.Helper()

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutting down: %v", err)
	}

	return boot(t, cfg)
}

func withAdministrator(cfg *configs.Application, password string) *configs.Application {
	cfg.Bootstrap.Admin = configs.BootstrapAdmin{Email: adminEmail, Password: password}

	return cfg
}

// find returns the lines carrying the fragment, so a caller can assert on how
// many there are as well as on what they say.
func find(lines []logLine, fragment string) []logLine {
	var found []logLine

	for _, line := range lines {
		if strings.Contains(line.Message, fragment) {
			found = append(found, line)
		}
	}

	return found
}

func assertLogged(t *testing.T, lines []logLine, fragment, level string) {
	t.Helper()

	found := find(lines, fragment)
	if len(found) != 1 {
		t.Fatalf("want one line saying %q, got %d in %+v", fragment, len(found), lines)
	}

	if found[0].Level != level {
		t.Errorf("%q: level = %q, want %q", fragment, found[0].Level, level)
	}
}

func assertNotLogged(t *testing.T, lines []logLine, fragments ...string) {
	t.Helper()

	for _, fragment := range fragments {
		if found := find(lines, fragment); len(found) != 0 {
			t.Errorf("want no line saying %q, got %+v", fragment, found)
		}
	}
}

func TestBootCreatesTheConfiguredAdministrator(t *testing.T) {
	app, lines := boot(t, withAdministrator(bootConfig(t), adminPassword))

	assertLogged(t, lines, msgCreated, "warn")
	assertLogged(t, lines, msgTemporary, "warn")
	assertNotLogged(t, lines, msgIgnored, msgNone)

	if got := find(lines, msgCreated)[0].Email; got != adminEmail {
		t.Errorf("email = %q, want %q", got, adminEmail)
	}

	master, err := app.realms.Resolve(t.Context(), "master")
	if err != nil {
		t.Fatalf("resolving the master realm: %v", err)
	}

	created, err := app.identities.FindByLogin(t.Context(), master.ID(), adminEmail)
	if err != nil {
		t.Fatalf("finding the administrator: %v", err)
	}

	if err := app.admins.Authorize(t.Context(), created.ID(), master.ID()); err != nil {
		t.Errorf("want the administrator authorized over the master realm, got %v", err)
	}
}

func TestALaterBootIgnoresTheConfiguredCredentials(t *testing.T) {
	cases := map[string]string{
		"the same password":    adminPassword,
		"a different password": adminNextPassword,
	}

	for name, password := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := withAdministrator(bootConfig(t), adminPassword)

			app, _ := boot(t, cfg)
			app, lines := reboot(t, app, withAdministrator(cfg, password))

			assertLogged(t, lines, msgIgnored, "info")
			assertLogged(t, lines, msgTemporary, "warn")
			assertNotLogged(t, lines, msgCreated, msgNone)

			master, err := app.realms.Resolve(t.Context(), "master")
			if err != nil {
				t.Fatalf("resolving the master realm: %v", err)
			}

			// Whatever was configured the second time, the first password stands.
			_, mustChange, err := app.credentials.VerifyPassword(t.Context(), master.ID(), adminEmail, adminPassword)
			if err != nil || !mustChange {
				t.Errorf("want the first password still accepted and temporary, got mustChange=%v err=%v", mustChange, err)
			}
		})
	}
}

func TestBootWithoutAnAdministratorSaysWhatToDo(t *testing.T) {
	_, lines := boot(t, bootConfig(t))

	assertLogged(t, lines, msgNone, "warn")
	assertNotLogged(t, lines, msgCreated, msgIgnored, msgTemporary)

	for _, want := range []string{
		"AEGIS_BOOTSTRAP_ADMIN_EMAIL", "AEGIS_BOOTSTRAP_ADMIN_PASSWORD", "aegisd bootstrap-admin",
	} {
		if got := find(lines, msgNone)[0].Message; !strings.Contains(got, want) {
			t.Errorf("want the warning to name %s, got %q", want, got)
		}
	}
}

func TestBootWithAnAdministratorAndNoConfigurationIsSilentAboutIt(t *testing.T) {
	cfg := bootConfig(t)

	app, _ := boot(t, withAdministrator(cfg, adminPassword))

	cfg.Bootstrap.Admin = configs.BootstrapAdmin{}

	_, lines := reboot(t, app, cfg)

	assertNotLogged(t, lines, msgNone, msgCreated, msgIgnored)
	assertLogged(t, lines, msgTemporary, "warn")
}
