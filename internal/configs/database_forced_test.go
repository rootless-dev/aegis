package configs

import (
	"slices"
	"testing"
)

// The counterpart of TestTheForcedParametersAreMirroredInConfigs in
// internal/infra/database: neither copy can prove the other right, but neither
// can change unnoticed. In package configs because the lists are unexported.
func TestTheForcedOptionKeysMatchTheDialects(t *testing.T) {
	serverKeys := []string{
		"charset", "loc", "multistatements", "parsetime",
		"sql_mode", "time_zone", "timeout", "tls",
	}

	cases := map[Driver][]string{
		DriverPostgres: {"connect_timeout", "sslmode", "sslrootcert", "timezone"},
		DriverMySQL:    serverKeys,
		DriverMariaDB:  serverKeys,
	}

	for driver, want := range cases {
		t.Run(driver.String(), func(t *testing.T) {
			got := slices.Clone(forcedOptionKeys[driver])
			slices.Sort(got)

			expected := slices.Clone(want)
			slices.Sort(expected)

			if !slices.Equal(got, expected) {
				t.Fatalf(
					"the forced option keys changed: got %v, want %v.\n"+
						"Mirror the change in internal/infra/database before updating this expectation",
					got, expected,
				)
			}
		})
	}
}

func TestTheForcedPragmaNamesMatchTheSQLiteDialect(t *testing.T) {
	got := make([]string, 0, len(sqliteForcedPragmaNames))
	for name := range sqliteForcedPragmaNames {
		got = append(got, name)
	}

	slices.Sort(got)

	want := []string{"busy_timeout", "foreign_keys", "journal_mode"}

	if !slices.Equal(got, want) {
		t.Fatalf(
			"the forced pragma names changed: got %v, want %v.\n"+
				"Mirror the change in internal/infra/database/sqlite.go before updating this expectation",
			got, want,
		)
	}
}

// A driver missing from forcedOptionKeys validates nothing: the lookup returns
// a nil slice and every option is accepted.
func TestEveryServerDriverHasForcedOptionKeys(t *testing.T) {
	for _, driver := range supportedDrivers {
		if driver.IsFileBased() {
			continue
		}

		if len(forcedOptionKeys[driver]) == 0 {
			t.Errorf("driver %q has no forced option keys, so its options are never validated", driver)
		}
	}
}
