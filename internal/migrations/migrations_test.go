package migrations_test

import (
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/migrations"
)

var dialects = []string{"postgres", "mysql", "mariadb", "sqlite"}

func TestForIsRootedAtTheDialectDirectory(t *testing.T) {
	for _, dialect := range dialects {
		tree, err := migrations.For(dialect)
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}

		// Rooted means the runner sees migration files, not a directory of
		// directories: iofs.New(tree, ".") has to find 0001 at the top.
		if _, err := fs.Stat(tree, "0001_create_realms.up.sql"); err != nil {
			t.Errorf("%s: 0001 is not at the root of the returned tree: %v", dialect, err)
		}
	}
}

func TestForRejectsAnUnknownDialect(t *testing.T) {
	if _, err := migrations.For("oracle"); err == nil {
		t.Fatal("an unknown dialect must be an error, not an empty tree")
	}
}

func TestLatestReportsTheHighestVersion(t *testing.T) {
	for _, dialect := range dialects {
		version, err := migrations.Latest(dialect)
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}

		if version != 1 {
			t.Errorf("%s: want 1, got %d", dialect, version)
		}
	}
}

func TestLatestRejectsAnUnknownDialect(t *testing.T) {
	// Zero is a real version meaning "empty database". Reporting it for a
	// directory that does not exist would make the boot's version check
	// compare against a lie.
	if _, err := migrations.Latest("oracle"); err == nil {
		t.Fatal("an unknown dialect must be an error, not version zero")
	}
}

// A version added to three directories and missed in the fourth leaves every
// engine job green: golang-migrate applies what it finds and stops.
//
// Compared by file name, not version number — stricter than golang-migrate
// needs, and deliberate: the name is the only description of a migration a
// reader has, and four directories naming one version differently is how a
// pair drifts apart unnoticed.
func TestEveryDialectCarriesTheSameVersions(t *testing.T) {
	reference := versionsIn(t, dialects[0])

	for _, dialect := range dialects[1:] {
		current := versionsIn(t, dialect)

		for name := range reference {
			if !current[name] {
				t.Errorf("%s is missing %s, which %s has", dialect, name, dialects[0])
			}
		}

		for name := range current {
			if !reference[name] {
				t.Errorf("%s has %s, which %s does not", dialect, name, dialects[0])
			}
		}
	}
}

// MultiStatements is off in the DSN, so a file with two statements fails on
// MySQL and MariaDB and passes on the other two: green on three engines, red
// on the two an on-prem customer is most likely to run.
func TestEveryMigrationHoldsExactlyOneStatement(t *testing.T) {
	for _, dialect := range dialects {
		tree, err := migrations.For(dialect)
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}

		entries, err := fs.ReadDir(tree, ".")
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}

		for _, entry := range entries {
			body, err := fs.ReadFile(tree, entry.Name())
			if err != nil {
				t.Fatalf("%s/%s: %v", dialect, entry.Name(), err)
			}

			statement := stripComments(string(body))

			if count := strings.Count(statement, ";"); count != 1 {
				t.Errorf("%s/%s: want exactly one statement, found %d semicolons", dialect, entry.Name(), count)
			}

			if !strings.HasSuffix(statement, ";") {
				t.Errorf("%s/%s: the statement must end at the last semicolon", dialect, entry.Name())
			}
		}
	}
}

func versionsIn(t *testing.T, dialect string) map[string]bool {
	t.Helper()

	tree, err := migrations.For(dialect)
	if err != nil {
		t.Fatalf("%s: %v", dialect, err)
	}

	entries, err := fs.ReadDir(tree, ".")
	if err != nil {
		t.Fatalf("%s: %v", dialect, err)
	}

	found := make(map[string]bool, len(entries))

	for _, entry := range entries {
		if path.Ext(entry.Name()) == ".sql" {
			found[entry.Name()] = true
		}
	}

	if len(found) == 0 {
		t.Fatalf("%s has no migrations at all", dialect)
	}

	return found
}

// Deliberately not a SQL parser: a semicolon inside a string literal would
// fool this. A migration that needs one is a migration worth reading by hand.
func stripComments(body string) string {
	var kept []string

	for line := range strings.Lines(body) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}

		kept = append(kept, trimmed)
	}

	return strings.TrimSpace(strings.Join(kept, "\n"))
}
