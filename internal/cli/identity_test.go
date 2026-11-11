package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/cli"
)

const (
	passwordVariable     = "AEGIS_IDENTITY_PASSWORD"
	passwordFileVariable = "AEGIS_IDENTITY_PASSWORD_FILE"
)

// Both forms are cleared first: a developer shell that exports one would make
// the "no password" cases pass or fail by machine.
func withPassword(t *testing.T, password string) {
	t.Helper()

	t.Setenv(passwordVariable, password)
	t.Setenv(passwordFileVariable, "")
}

func withoutPassword(t *testing.T) {
	t.Helper()

	withPassword(t, "")
}

func TestIdentityVerbsAreRecognised(t *testing.T) {
	withPassword(t, "correct horse")

	for _, args := range [][]string{
		{"identity", "create", "acme", "ana@example.com"},
		{"identity", "create", "acme", "ana@example.com", "ana"},
		{"identity", "set-password", "acme", "ana@example.com"},
		{"identity", "set-password", "acme", "ana"},
		{"identity", "list", "acme"},
		{"identity", "disable", "acme", "ana"},
		{"identity", "enable", "acme", "ana@example.com"},
	} {
		handled, remaining, run, usageErr := cli.Dispatch(args)

		if !handled || run == nil {
			t.Errorf("%v must be recognised", args)
		}

		if usageErr != nil {
			t.Errorf("%v: usageErr: got %v, want nil", args, usageErr)
		}

		if len(remaining) != 0 {
			t.Errorf("%v: remaining: want none, got %v", args, remaining)
		}
	}
}

func TestIdentityVerbsRefuseMissingArguments(t *testing.T) {
	withPassword(t, "correct horse")

	for _, args := range [][]string{
		{"identity"},
		{"identity", "create"},
		{"identity", "create", "acme"},
		{"identity", "create", "acme", "--dev"},
		{"identity", "set-password"},
		{"identity", "set-password", "acme"},
		{"identity", "list"},
		{"identity", "list", "--dev"},
		{"identity", "disable"},
		{"identity", "disable", "acme"},
		{"identity", "enable"},
		{"identity", "enable", "acme"},
	} {
		handled, _, run, usageErr := cli.Dispatch(args)

		if !handled {
			t.Errorf("%v must not fall through to serving", args)
		}

		if usageErr == nil {
			t.Errorf("%v: a missing argument must be reported as a usage error", args)
		}

		if run != nil {
			t.Errorf("%v: a missing argument must not return a runner", args)
		}
	}
}

func TestIdentityVerbsRefuseStrayTokens(t *testing.T) {
	withPassword(t, "correct horse")

	for _, args := range [][]string{
		{"identity", "create", "acme", "ana@example.com", "ana", "extra"},
		{"identity", "set-password", "acme", "ana", "extra"},
		{"identity", "list", "acme", "extra"},
		{"identity", "disable", "acme", "ana", "extra"},
		{"identity", "enable", "acme", "ana", "extra"},
	} {
		_, _, run, usageErr := cli.Dispatch(args)

		if usageErr == nil || !strings.Contains(usageErr.Error(), `does not take "extra"`) {
			t.Errorf("%v: want the stray token named, got %v", args, usageErr)
		}

		if run != nil {
			t.Errorf("%v: a stray token must not return a runner", args)
		}
	}
}

func TestDispatchUnknownIdentityVerb(t *testing.T) {
	_, _, run, err := cli.Dispatch([]string{"identity", "frobnicate"})

	if err == nil || run != nil {
		t.Fatalf("want a usage error and no runner, got run=%v err=%v", run != nil, err)
	}
}

// --temporary belongs to the verb. Left in what goes back to the configuration
// builder it would be an unknown flag, and left in place before a positional
// argument it would read as "the arguments are over".
func TestTemporaryIsConsumedWhereverItSits(t *testing.T) {
	withPassword(t, "correct horse")

	for _, args := range [][]string{
		{"identity", "create", "--temporary", "acme", "ana@example.com", "ana", "--dev"},
		{"identity", "create", "acme", "--temporary", "ana@example.com", "ana", "--dev"},
		{"identity", "create", "acme", "ana@example.com", "--temporary", "ana", "--dev"},
		{"identity", "create", "acme", "ana@example.com", "ana", "--temporary", "--dev"},
		{"identity", "create", "acme", "ana@example.com", "ana", "--dev", "--temporary"},
		{"identity", "create", "acme", "ana@example.com", "--dev", "-temporary"},
		{"identity", "create", "acme", "ana@example.com", "--dev", "--temporary=true"},
		{"identity", "create", "acme", "ana@example.com", "--dev", "--temporary=false"},
		{"identity", "set-password", "--temporary", "acme", "ana", "--dev"},
		{"identity", "set-password", "acme", "--temporary", "ana", "--dev"},
		{"identity", "set-password", "acme", "ana", "--temporary", "--dev"},
		{"identity", "set-password", "acme", "ana", "--dev", "--temporary"},
	} {
		_, remaining, run, usageErr := cli.Dispatch(args)

		if usageErr != nil {
			t.Errorf("%v: usageErr: got %v, want nil", args, usageErr)
		}

		if run == nil {
			t.Errorf("%v: a runner must be returned", args)
		}

		if !slices.Equal(remaining, []string{"--dev"}) {
			t.Errorf("%v: remaining: want [--dev], got %v", args, remaining)
		}
	}
}

func TestTemporaryRefusesAValueThatIsNotABoolean(t *testing.T) {
	withPassword(t, "correct horse")

	_, _, run, err := cli.Dispatch([]string{"identity", "set-password", "acme", "ana", "--temporary=maybe"})

	if err == nil || run != nil {
		t.Fatalf("want a usage error and no runner, got run=%v err=%v", run != nil, err)
	}
}

// The verbs that set no password leave the flag to the builder, which refuses
// it: silently accepting it would suggest it did something.
func TestTemporaryIsNotConsumedByTheOtherVerbs(t *testing.T) {
	for _, args := range [][]string{
		{"identity", "list", "acme", "--temporary"},
		{"identity", "disable", "acme", "ana", "--temporary"},
		{"identity", "enable", "acme", "ana", "--temporary"},
	} {
		_, remaining, _, usageErr := cli.Dispatch(args)

		if usageErr != nil {
			t.Errorf("%v: usageErr: got %v, want nil", args, usageErr)
		}

		if !slices.Equal(remaining, []string{"--temporary"}) {
			t.Errorf("%v: remaining: want [--temporary], got %v", args, remaining)
		}
	}
}

func TestSetPasswordWithoutAPasswordNamesBothVariables(t *testing.T) {
	withoutPassword(t)

	_, _, run, err := cli.Dispatch([]string{"identity", "set-password", "acme", "ana"})

	if run != nil {
		t.Error("want no runner")
	}

	if err == nil {
		t.Fatal("want a usage error, got none")
	}

	for _, name := range []string{passwordVariable, passwordFileVariable} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("want the message to name %s, got %q", name, err)
		}
	}
}

func TestCreateWithoutAPasswordIsAccepted(t *testing.T) {
	withoutPassword(t)

	_, _, run, err := cli.Dispatch([]string{"identity", "create", "acme", "ana@example.com"})

	if err != nil || run == nil {
		t.Fatalf("want a runner, got run=%v err=%v", run != nil, err)
	}
}

func TestCreateRefusesTemporaryWithoutAPassword(t *testing.T) {
	withoutPassword(t)

	_, _, run, err := cli.Dispatch([]string{"identity", "create", "acme", "ana@example.com", "--temporary"})

	if err == nil || run != nil {
		t.Fatalf("want a usage error and no runner, got run=%v err=%v", run != nil, err)
	}
}

func TestThePasswordIsReadFromAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("correct horse\n"), 0o600); err != nil {
		t.Fatalf("writing the password file: %v", err)
	}

	t.Setenv(passwordVariable, "")
	t.Setenv(passwordFileVariable, path)

	_, _, run, err := cli.Dispatch([]string{"identity", "set-password", "acme", "ana"})

	if err != nil || run == nil {
		t.Fatalf("want a runner, got run=%v err=%v", run != nil, err)
	}
}

func TestBothPasswordVariablesSetIsRefused(t *testing.T) {
	t.Setenv(passwordVariable, "correct horse")
	t.Setenv(passwordFileVariable, filepath.Join(t.TempDir(), "password"))

	for _, args := range [][]string{
		{"identity", "create", "acme", "ana@example.com"},
		{"identity", "set-password", "acme", "ana"},
	} {
		_, _, run, err := cli.Dispatch(args)

		if err == nil || run != nil {
			t.Errorf("%v: want a usage error and no runner, got run=%v err=%v", args, run != nil, err)
		}
	}
}

func TestAnUnreadablePasswordFileIsRefused(t *testing.T) {
	t.Setenv(passwordVariable, "")
	t.Setenv(passwordFileVariable, filepath.Join(t.TempDir(), "missing"))

	_, _, run, err := cli.Dispatch([]string{"identity", "set-password", "acme", "ana"})

	if err == nil || run != nil {
		t.Fatalf("want a usage error and no runner, got run=%v err=%v", run != nil, err)
	}
}
