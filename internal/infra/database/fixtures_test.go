package database

import (
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
)

// engineDDL is all the migration fixtures differ by, engine to engine.
type engineDDL struct {
	idType   string
	textType string

	// tableSuffix is what MySQL and MariaDB spell after the closing paren.
	tableSuffix string
}

const innoDBSuffix = " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"

var engineDDLs = map[Driver]engineDDL{
	DriverPostgres: {idType: "INTEGER", textType: "VARCHAR(64)"},
	DriverSQLite:   {idType: "INTEGER", textType: "TEXT"},
	DriverMySQL:    {idType: "INT", textType: "VARCHAR(64)", tableSuffix: innoDBSuffix},
	DriverMariaDB:  {idType: "INT", textType: "VARCHAR(64)", tableSuffix: innoDBSuffix},
}

func sqlFile(body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(body)}
}

// probeTable is version 1 of every set below.
func probeTable(ddl engineDDL) fstest.MapFS {
	return fstest.MapFS{
		"000001_probe.up.sql": sqlFile(fmt.Sprintf(
			"CREATE TABLE migration_probe (\n    id    %s NOT NULL PRIMARY KEY,\n    label %s NOT NULL\n)%s;\n",
			ddl.idType, ddl.textType, ddl.tableSuffix,
		)),
		"000001_probe.down.sql": sqlFile("DROP TABLE migration_probe;\n"),
	}
}

// migrationFixtures is the healthy set. Both directions are present, since
// the reversibility test drives Down.
func migrationFixtures(driver Driver) fs.FS {
	ddl := engineDDLs[driver]
	set := probeTable(ddl)

	set["000002_probe_column.up.sql"] = sqlFile(
		fmt.Sprintf("ALTER TABLE migration_probe ADD COLUMN note %s;\n", ddl.textType),
	)
	set["000002_probe_column.down.sql"] = sqlFile("ALTER TABLE migration_probe DROP COLUMN note;\n")

	return set
}

// brokenFixtures fails on version 2 by adding a column version 1 created.
func brokenFixtures(driver Driver) fs.FS {
	ddl := engineDDLs[driver]
	set := probeTable(ddl)

	set["000002_probe_broken.up.sql"] = sqlFile(
		fmt.Sprintf("ALTER TABLE migration_probe ADD COLUMN label %s;\n", ddl.textType),
	)

	return set
}

// brokenMultiFixtures fails behind a statement that would have succeeded on
// its own: extra_column must not exist once this set fails on label.
func brokenMultiFixtures(driver Driver) fs.FS {
	ddl := engineDDLs[driver]
	set := probeTable(ddl)

	set["000002_probe_broken.up.sql"] = sqlFile(fmt.Sprintf(
		"ALTER TABLE migration_probe ADD COLUMN extra_column %s;\nALTER TABLE migration_probe ADD COLUMN label %s;\n",
		ddl.textType, ddl.textType,
	))

	return set
}

// The builders above index engineDDLs without checking the lookup.
func TestEveryDriverHasFixtureDDL(t *testing.T) {
	for driver := range dialects() {
		if _, ok := engineDDLs[driver]; !ok {
			t.Errorf("driver %q is supported but has no fixture ddl", driver)
		}
	}
}
