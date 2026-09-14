package repository

// In-package, like internal/configs/database_forced_test.go: translate maps
// driver text no caller can produce through the repository API.

import (
	"errors"
	"testing"

	"github.com/rootless-dev/aegis/internal/domain/key"
	"github.com/rootless-dev/aegis/internal/domain/realm"
)

// SQLite names the same column on a NOT NULL violation as on a UNIQUE one, so
// matching the column alone would report "already in use" for a write that is
// not a duplicate.
func TestTranslateOnlyReadsAUniqueViolationAsATakenValue(t *testing.T) {
	tests := map[string]struct {
		message string
		want    error
	}{
		"a unique violation on the slug":   {"UNIQUE constraint failed: realms.slug", realm.ErrSlugTaken},
		"a unique violation on the issuer": {"UNIQUE constraint failed: realms.issuer", realm.ErrIssuerTaken},
		"a not null violation on the slug": {"NOT NULL constraint failed: realms.slug", nil},
		"a check violation on the issuer":  {"CHECK constraint failed: realms.issuer", nil},

		"a unique violation on the active marker": {
			"UNIQUE constraint failed: realm_keys.realm_id, realm_keys.purpose, realm_keys.algorithm, realm_keys.active_marker",
			key.ErrActiveKeyExists,
		},
		"a unique violation on the kid": {
			"UNIQUE constraint failed: realm_keys.realm_id, realm_keys.kid",
			key.ErrKIDTaken,
		},
		"a check violation on the marker": {"CHECK constraint failed: ck_realm_keys_marker", nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			original := errors.New(tc.message)

			got := translate(original, realm.ErrNotFound)

			if tc.want == nil {
				if !errors.Is(got, original) {
					t.Errorf("want the driver error passed through, got %v", got)
				}

				return
			}

			if !errors.Is(got, tc.want) {
				t.Errorf("want %v, got %v", tc.want, got)
			}
		})
	}
}
