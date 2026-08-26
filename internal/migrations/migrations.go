// Package migrations owns the SQL that builds the aegis schema, one directory
// per dialect. It is separate from the runner, which takes its source as a
// parameter.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// MariaDB has its own directory even though it is identical to MySQL today:
// splitting it later is impossible for an installation that already migrated.
//
//go:embed postgres/*.sql mysql/*.sql mariadb/*.sql sqlite/*.sql
var files embed.FS

var supported = map[string]bool{
	"postgres": true,
	"mysql":    true,
	"mariadb":  true,
	"sqlite":   true,
}

// For returns one dialect's migrations, rooted at its directory so the runner
// sees files rather than directories.
func For(driver string) (fs.FS, error) {
	if !supported[driver] {
		return nil, fmt.Errorf("migrations: no migrations for driver %q", driver)
	}

	tree, err := fs.Sub(files, driver)
	if err != nil {
		return nil, fmt.Errorf("migrations: rooting %q: %w", driver, err)
	}

	return tree, nil
}

// Latest reports the highest version a dialect carries. Derived rather than
// declared, because a constant is one more thing to forget to bump.
func Latest(driver string) (uint, error) {
	tree, err := For(driver)
	if err != nil {
		return 0, err
	}

	entries, err := fs.ReadDir(tree, ".")
	if err != nil {
		return 0, fmt.Errorf("migrations: reading %q: %w", driver, err)
	}

	var highest uint

	var found bool

	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}

		version, err := versionOf(entry.Name())
		if err != nil {
			return 0, err
		}

		found = true

		if version > highest {
			highest = version
		}
	}

	// Zero already means "nothing applied yet", so an empty directory has to be
	// an error rather than that.
	if !found {
		return 0, fmt.Errorf("migrations: %q has no migrations", driver)
	}

	return highest, nil
}

func versionOf(name string) (uint, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migrations: %q does not start with a version", name)
	}

	version, err := strconv.ParseUint(prefix, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("migrations: %q does not start with a version: %w", name, err)
	}

	return uint(version), nil
}
