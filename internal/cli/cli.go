// Package cli holds the subcommand dispatch. It lives here rather than in cmd/
// because cmd/** is excluded from coverage and dispatch has branches worth
// measuring.
package cli

import (
	"fmt"
	"strconv"

	"github.com/rootless-dev/aegis/internal/configs"
)

// Runner returns a process exit code rather than an error: migrate status uses
// three distinct ones.
type Runner func(cfg *configs.Application) int

// Dispatch decides whether the command line names a subcommand. It must be the
// first argument; anything starting with a dash means the process serves.
//
// usageErr is returned instead of a Runner so the caller can exit before the
// configuration builder runs: naming an unknown subcommand must not first
// require a reachable database.
func Dispatch(args []string) (handled bool, remaining []string, run Runner, usageErr error) {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return false, args, nil, nil
	}

	switch args[0] {
	case "migrate":
		remaining, run, usageErr := dispatchMigrate(args[1:])

		return true, remaining, run, usageErr
	default:
		return true, nil, nil, fmt.Errorf("aegisd: unknown command %q\n\n%s", args[0], usage)
	}
}

const usage = `Usage:
  aegisd [flags]                    serve
  aegisd migrate [flags]            apply pending migrations
  aegisd migrate status [flags]     report the schema version; exits 1 when behind, 2 when dirty
  aegisd migrate force <n> [flags]  record version n and clear the dirty flag

The subcommand must come first; flags follow it.
`

func parseVersion(raw string) (int, error) {
	version, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("aegisd: %q is not a version number", raw)
	}

	if version < 0 {
		return 0, fmt.Errorf("aegisd: version cannot be negative, got %d", version)
	}

	return version, nil
}
