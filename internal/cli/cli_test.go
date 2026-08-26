package cli_test

import (
	"testing"

	"github.com/rootless-dev/aegis/internal/cli"
)

func TestNoArgumentsIsNotASubcommand(t *testing.T) {
	handled, remaining, run, usageErr := cli.Dispatch(nil)

	if handled {
		t.Error("an empty command line must serve")
	}

	if run != nil {
		t.Error("nothing should be returned to run")
	}

	if usageErr != nil {
		t.Errorf("usageErr: got %v, want nil", usageErr)
	}

	if len(remaining) != 0 {
		t.Errorf("remaining: got %v", remaining)
	}
}

// Go's flag package stops parsing at the first non-flag argument, so handing
// "migrate --dev" straight to the builder would yield zero flags and swallow
// the --dev in silence. The subcommand has to be stripped first.
func TestASubcommandIsStrippedAndItsFlagsSurvive(t *testing.T) {
	handled, remaining, run, usageErr := cli.Dispatch([]string{"migrate", "--dev"})

	if !handled {
		t.Fatal("migrate must be recognised")
	}

	if run == nil {
		t.Fatal("a runner must be returned")
	}

	if usageErr != nil {
		t.Errorf("usageErr: got %v, want nil", usageErr)
	}

	if len(remaining) != 1 || remaining[0] != "--dev" {
		t.Errorf("remaining: want [--dev], got %v", remaining)
	}
}

func TestTheSubcommandMustBeTheFirstArgument(t *testing.T) {
	// Flags first is not a migration. This is the usual convention, and it
	// keeps the rule to one line — but it is a rule someone will trip over.
	handled, _, _, _ := cli.Dispatch([]string{"--dev", "migrate"})

	if handled {
		t.Error("a leading flag means the process serves")
	}
}

func TestMigrateVerbsAreRecognised(t *testing.T) {
	for _, args := range [][]string{
		{"migrate"},
		{"migrate", "status"},
		{"migrate", "force", "3"},
	} {
		handled, _, run, usageErr := cli.Dispatch(args)

		if !handled || run == nil {
			t.Errorf("%v must be recognised", args)
		}

		if usageErr != nil {
			t.Errorf("%v: usageErr: got %v, want nil", args, usageErr)
		}
	}
}

// An unknown subcommand is an argument error, not a configuration error: it
// must be reported without ever touching the configuration builder or a
// database, so it comes back as usageErr with a nil Runner rather than as a
// Runner that fails once called.
func TestAnUnknownSubcommandIsHandledAndFails(t *testing.T) {
	handled, _, run, usageErr := cli.Dispatch([]string{"frobnicate"})

	if !handled {
		t.Fatal("an unknown subcommand must not fall through to serving")
	}

	if usageErr == nil {
		t.Error("an unknown subcommand must be reported as a usage error")
	}

	if run != nil {
		t.Error("an unknown subcommand must not return a runner")
	}
}

func TestForceRefusesANonNumericVersion(t *testing.T) {
	_, _, run, usageErr := cli.Dispatch([]string{"migrate", "force", "latest"})

	if usageErr == nil {
		t.Error("a non-numeric version must be reported as a usage error")
	}

	if run != nil {
		t.Error("a non-numeric version must not return a runner")
	}
}

func TestForceRefusesANegativeVersion(t *testing.T) {
	_, _, run, usageErr := cli.Dispatch([]string{"migrate", "force", "-1"})

	if usageErr == nil {
		t.Error("a negative version must be reported as a usage error")
	}

	if run != nil {
		t.Error("a negative version must not return a runner")
	}
}

// flag.Parse stops at the first non-flag argument, so `aegisd migrate
// frobnicate` would otherwise apply migrations. A typo one word after the
// subcommand has to mean what `aegisd frobnicate` already means.
func TestAStrayTokenAfterMigrateIsAUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"migrate", "frobnicate"},
		{"migrate", "status", "extra"},
		{"migrate", "force", "1", "extra"},
	} {
		handled, _, run, usageErr := cli.Dispatch(args)

		if !handled {
			t.Errorf("%v must not fall through to serving", args)
		}

		if usageErr == nil {
			t.Errorf("%v: a stray token must be reported as a usage error", args)
		}

		if run != nil {
			t.Errorf("%v: a stray token must not return a runner", args)
		}
	}
}

// The rule is about the first token only: everything from the first dash on is
// the configuration builder's business, and rejecting it here would be a second
// flag parser disagreeing with the real one.
func TestFlagsStillSurviveEveryMigrateVerb(t *testing.T) {
	for _, args := range [][]string{
		{"migrate", "--dev"},
		{"migrate", "status", "--dev"},
		{"migrate", "force", "3", "--dev"},
	} {
		_, remaining, run, usageErr := cli.Dispatch(args)

		if usageErr != nil {
			t.Errorf("%v: usageErr: got %v, want nil", args, usageErr)
		}

		if run == nil {
			t.Errorf("%v: a runner must be returned", args)
		}

		if len(remaining) != 1 || remaining[0] != "--dev" {
			t.Errorf("%v: remaining: want [--dev], got %v", args, remaining)
		}
	}
}
