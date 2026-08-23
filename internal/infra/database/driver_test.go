package database

import (
	"slices"
	"testing"
)

// These lists are duplicated in internal/configs, which cannot import this
// package. This fails on purpose when one changes: mirror it there, then
// update the expectation here.
func TestTheForcedParametersAreMirroredInConfigs(t *testing.T) {
	cases := map[string]struct {
		forced map[string]bool
		want   []string
	}{
		"postgres": {
			forced: postgresReservedParams,
			want:   []string{"connect_timeout", "sslmode", "sslrootcert", "timezone"},
		},
		"mysql": {
			forced: mysqlReservedParams,
			want: []string{
				"charset", "loc", "multistatements", "parsetime",
				"sql_mode", "time_zone", "timeout", "tls",
			},
		},
		"sqlite": {
			forced: sqliteForcedPragmas,
			want:   []string{"busy_timeout", "foreign_keys", "journal_mode"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := make([]string, 0, len(tc.forced))
			for key := range tc.forced {
				got = append(got, key)
			}

			slices.Sort(got)

			if !slices.Equal(got, tc.want) {
				t.Fatalf(
					"the forced parameters changed: got %v, want %v.\n"+
						"Mirror the change in internal/configs/database.go before updating this expectation",
					got, tc.want,
				)
			}
		})
	}
}
