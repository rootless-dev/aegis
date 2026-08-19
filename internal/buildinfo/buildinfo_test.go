package buildinfo_test

import (
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/buildinfo"
)

func TestReadReportsTheBuildIdentity(t *testing.T) {
	info := buildinfo.Read()

	// Without -X the version falls back instead of coming out empty, so a log
	// line never shows a blank version.
	if info.Version == "" {
		t.Error("version must never be empty")
	}

	if info.GoVersion == "" {
		t.Error("go version should come from the build info")
	}

	t.Logf("version=%q revision=%q built_at=%q dirty=%v go=%s",
		info.Version, info.ShortRevision(), info.Time, info.Modified, info.GoVersion)
}

func TestShortRevisionTrimsAndSurvivesShortValues(t *testing.T) {
	cases := map[string]struct {
		revision string
		want     string
	}{
		"full sha":  {"d41b1af1082403116945f70eb97d6f5c9a1a9812", "d41b1af"},
		"unknown":   {"unknown", "unknown"},
		"empty":     {"", ""},
		"too short": {"abc", "abc"},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := buildinfo.Info{Revision: test.revision}.ShortRevision()
			if got != test.want {
				t.Errorf("want %q, got %q", test.want, got)
			}
		})
	}
}

func TestReadIsStable(t *testing.T) {
	first := buildinfo.Read()
	second := buildinfo.Read()

	if first != second {
		t.Error("repeated reads must return the same value")
	}
}

func TestVersionIsNotTakenFromTheEnvironment(t *testing.T) {
	// The whole point of the package: a deployment must not be able to claim a
	// version other than the one that was built.
	t.Setenv("APP_VERSION", "v9.9.9")

	if strings.Contains(buildinfo.Read().Version, "9.9.9") {
		t.Error("the environment must not influence the reported version")
	}
}
