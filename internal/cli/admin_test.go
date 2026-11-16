package cli_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/rootless-dev/aegis/internal/cli"
)

func TestAdminVerbsAreRecognised(t *testing.T) {
	for _, args := range [][]string{
		{"bootstrap-admin"},
		{"admin", "list"},
		{"admin", "grant", "ana@example.com"},
		{"admin", "grant", "ana", "acme"},
		{"admin", "revoke", "ana@example.com"},
		{"admin", "revoke", "ana", "acme"},
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

func TestAdminVerbsRefuseMissingArguments(t *testing.T) {
	for _, args := range [][]string{
		{"admin"},
		{"admin", "grant"},
		{"admin", "grant", "--dev"},
		{"admin", "revoke"},
		{"admin", "revoke", "--dev"},
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

// The first case is the one that matters: a password typed after the command
// must be refused, not ignored.
func TestAdminVerbsRefuseStrayTokens(t *testing.T) {
	for _, args := range [][]string{
		{"bootstrap-admin", "extra"},
		{"admin", "list", "extra"},
		{"admin", "grant", "ana", "acme", "extra"},
		{"admin", "revoke", "ana", "acme", "extra"},
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

func TestDispatchUnknownAdminVerb(t *testing.T) {
	_, _, run, err := cli.Dispatch([]string{"admin", "frobnicate"})

	if err == nil || run != nil {
		t.Fatalf("want a usage error and no runner, got run=%v err=%v", run != nil, err)
	}
}
