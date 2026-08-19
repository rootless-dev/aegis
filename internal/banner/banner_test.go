package banner_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/banner"
)

func TestPrintWritesNothingWhenDisabled(t *testing.T) {
	var out bytes.Buffer

	banner.Print(&out, false)

	// Anything at all here would reach a log collector that expects one
	// structured entry per line.
	if out.Len() != 0 {
		t.Errorf("a disabled banner must write nothing, got %q", out.String())
	}
}

func TestPrintReportsTheBinaryIdentity(t *testing.T) {
	var out bytes.Buffer

	banner.Print(&out, true)

	printed := out.String()

	if !strings.Contains(printed, "identity & access management") {
		t.Error("the tagline should be printed")
	}

	// Version and platform are the reason the banner is worth having at all.
	for _, want := range []string{"devel", "go1.", "/"} {
		if !strings.Contains(printed, want) {
			t.Errorf("banner should mention %q, got:\n%s", want, printed)
		}
	}

	if lines := strings.Count(printed, "\n"); lines < 8 {
		t.Errorf("banner looks truncated, got %d lines:\n%s", lines, printed)
	}
}

func TestPrintFlagsADirtyBuild(t *testing.T) {
	var out bytes.Buffer

	banner.Print(&out, true)

	// The tests run from a working tree, so this build carries no clean commit
	// and the banner has to say so rather than look official.
	if !strings.Contains(out.String(), "dirty") {
		t.Logf("build is not marked dirty, which is expected only from a clean tree:\n%s", out.String())
	}
}
