package configbuilder

import (
	"flag"

	"github.com/rootless-dev/aegis/internal/configs"
)

// flags carries what the command line actually declared. The fields are
// pointers because a bool flag that was left out is indistinguishable by value
// from one passed as false, and an absent flag must not overwrite what the file
// or the environment set.
type flags struct {
	dev *bool

	// migrateOnBoot is a pointer for the same reason dev is: a bool flag left
	// out is indistinguishable by value from one passed as false.
	migrateOnBoot *bool
}

// parseFlags reads args into flags. It builds a FlagSet of its own rather than
// using the package level one, so parsing is a function of its arguments and a
// test can exercise it without touching the command line of the test binary.
//
// ExitOnError keeps the behaviour any CLI is expected to have: --help prints the
// usage and exits 0, an unknown flag prints it and exits 2. Neither is a
// configuration problem, so neither belongs in the chain that reports those.
func parseFlags(args []string) flags {
	set := flag.NewFlagSet("aegis", flag.ExitOnError)

	dev := set.Bool("dev", false,
		"run under the development profile: TLS is served from a certificate generated in memory and no topology has to be declared")
	migrateOnBoot := set.Bool("migrate-on-boot", true,
		"apply pending migrations during startup; --migrate-on-boot=false leaves that to `aegisd migrate`")

	_ = set.Parse(args)

	var parsed flags

	set.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "dev":
			parsed.dev = dev
		case "migrate-on-boot":
			parsed.migrateOnBoot = migrateOnBoot
		}
	})

	return parsed
}

func applyFlags(cfg *configs.Application, parsed flags) {
	if parsed.dev != nil {
		cfg.Profile = configs.ProfileProd

		if *parsed.dev {
			cfg.Profile = configs.ProfileDev
		}
	}

	if parsed.migrateOnBoot != nil && cfg.Database != nil && cfg.Database.Migrate != nil {
		cfg.Database.Migrate.OnBoot = *parsed.migrateOnBoot
	}
}
