package cli_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/cli"
)

const sessionID = "0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

func TestSessionVerbsAreRecognised(t *testing.T) {
	for _, args := range [][]string{
		{"session", "list", "acme", "ana@example.com"},
		{"session", "list", "acme", "ana"},
		{"session", "revoke", "acme", sessionID},
		{"session", "revoke-all", "acme", "ana"},
	} {
		handled, remaining, run, usageErr := cli.Dispatch(append(args, "--dev"))

		if !handled || run == nil {
			t.Errorf("%v must be recognised", args)
		}

		if usageErr != nil {
			t.Errorf("%v: usageErr: got %v, want nil", args, usageErr)
		}

		if !slices.Equal(remaining, []string{"--dev"}) {
			t.Errorf("%v: remaining: want [--dev], got %v", args, remaining)
		}
	}
}

func TestSessionVerbsRefuseMissingArguments(t *testing.T) {
	for _, args := range [][]string{
		{"session"},
		{"session", "list"},
		{"session", "list", "acme"},
		{"session", "list", "acme", "--dev"},
		{"session", "revoke"},
		{"session", "revoke", "acme"},
		{"session", "revoke", "acme", "--dev"},
		{"session", "revoke-all"},
		{"session", "revoke-all", "acme"},
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

func TestSessionVerbsRefuseStrayTokens(t *testing.T) {
	for _, args := range [][]string{
		{"session", "list", "acme", "ana", "extra"},
		{"session", "revoke", "acme", sessionID, "extra"},
		{"session", "revoke-all", "acme", "ana", "extra"},
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

// A usage error rather than a runner that fails: a mistyped id must not need a
// reachable database to be reported.
func TestSessionRevokeRefusesAnIDThatIsNotAUUID(t *testing.T) {
	for _, raw := range []string{"ana", "0193a1b2", sessionID + "0"} {
		_, _, run, usageErr := cli.Dispatch([]string{"session", "revoke", "acme", raw})

		if usageErr == nil || !strings.Contains(usageErr.Error(), "is not a session id") {
			t.Errorf("%q: want the id refused, got %v", raw, usageErr)
		}

		if run != nil {
			t.Errorf("%q: a malformed id must not return a runner", raw)
		}
	}
}

func TestDispatchUnknownSessionVerb(t *testing.T) {
	_, _, run, err := cli.Dispatch([]string{"session", "frobnicate"})

	if err == nil || run != nil {
		t.Fatalf("want a usage error and no runner, got run=%v err=%v", run != nil, err)
	}
}
