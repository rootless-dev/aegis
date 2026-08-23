package configs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rootless-dev/aegis/internal/configs"
)

// serverDatabase returns a section valid for a server-backed driver, so each
// case only has to break the one rule it is about.
func serverDatabase() *configs.Database {
	cfg := configs.Default().Database
	cfg.Driver = configs.DriverPostgres
	cfg.Host = "db.internal"
	cfg.Name = "aegis"
	cfg.User = "aegis"
	cfg.Password = "secret"

	// Declared because production requires the decision to be explicit, not
	// because this fixture cares which way it goes.
	cfg.SSLMode = "require"

	return cfg
}

func TestDatabaseValidationMatrix(t *testing.T) {
	cases := map[string]struct {
		profile configs.Profile
		arrange func(cfg *configs.Database)
		wants   string
	}{
		"undeclared driver is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Driver = "" },
			wants:   "driver is required",
		},
		"unknown driver is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Driver = "oracle" },
			wants:   `unsupported driver "oracle"`,
		},
		"sqlite in production is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password, cfg.SSLMode = "", "", "", "", ""
				cfg.Path = "/var/lib/aegis/aegis.db"
			},
			wants: "development engine",
		},
		"sqlite in development is accepted": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password, cfg.SSLMode = "", "", "", "", ""
				cfg.Path = "./aegis.dev.db"
			},
		},
		"sqlite without a path is refused": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password, cfg.SSLMode = "", "", "", "", ""
			},
			wants: "path is required",
		},
		"sqlite carrying a host is refused": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Name, cfg.User, cfg.Password = "", "", ""
				cfg.Path = "./aegis.dev.db"
			},
			wants: "file based, so host cannot be set",
		},
		"a server driver carrying a path is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Path = "./aegis.db" },
			wants:   "connects to a server, so path cannot be set",
		},
		"a missing host is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Host = "" },
			wants:   "host is empty",
		},
		"a missing database name is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Name = "" },
			wants:   "name is empty",
		},
		"a missing user is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.User = "" },
			wants:   "user is empty",
		},
		"an out of range port is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Port = "70000" },
			wants:   `invalid port "70000"`,
		},
		"an unknown ssl mode is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.SSLMode = "sorta" },
			wants:   `unsupported ssl mode "sorta"`,
		},
		// An unset mode is "prefer" by the time the dialect sees it, and prefer
		// silently accepts a plaintext connection. Production has to say which
		// it meant.
		"an undeclared ssl mode is refused in production": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.SSLMode = "" },
			wants:   "ssl_mode is required",
		},
		"disable is a declaration, and is accepted": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.SSLMode = "disable" },
		},
		"an undeclared ssl mode is accepted in development": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) { cfg.SSLMode = "" },
		},
		"a ca file without verification is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.SSLMode = "require"
				cfg.SSLRootCert = "/etc/aegis/db-ca.pem"
			},
			wants: "only verified by ssl_mode",
		},
		"a ca file with verify-full is accepted": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.SSLMode = "verify-full"
				cfg.SSLRootCert = "/etc/aegis/db-ca.pem"
			},
		},
		"an option colliding with a forced parameter is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Options = map[string]string{"TimeZone": "America/Sao_Paulo"}
			},
			wants: "is set by aegis and cannot be overridden",
		},
		"an unrelated option is accepted": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Options = map[string]string{"application_name": "aegis"}
			},
		},
		"a postgres connect_timeout option is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Options = map[string]string{"connect_timeout": "999"}
			},
			wants: "is set by aegis and cannot be overridden",
		},
		"a mysql timeout option is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverMySQL
				cfg.Options = map[string]string{"timeout": "999s"}
			},
			wants: "is set by aegis and cannot be overridden",
		},
		"a sqlite option naming a forced pragma is refused": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password, cfg.SSLMode = "", "", "", "", ""
				cfg.Path = "./aegis.dev.db"
				cfg.Options = map[string]string{"_pragma": "busy_timeout(1000)"}
			},
			wants: `pragma "busy_timeout" is set by aegis and cannot be overridden`,
		},
		"a sqlite option naming a forced pragma in a different case is refused": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password, cfg.SSLMode = "", "", "", "", ""
				cfg.Path = "./aegis.dev.db"
				cfg.Options = map[string]string{"_pragma": "BUSY_TIMEOUT(1000)"}
			},
			wants: `pragma "busy_timeout" is set by aegis and cannot be overridden`,
		},
		"a sqlite option naming a benign pragma is accepted": {
			profile: configs.ProfileDev,
			arrange: func(cfg *configs.Database) {
				cfg.Driver = configs.DriverSQLite
				cfg.Host, cfg.Name, cfg.User, cfg.Password, cfg.SSLMode = "", "", "", "", ""
				cfg.Path = "./aegis.dev.db"
				cfg.Options = map[string]string{"_pragma": "cache_size(-2000)"}
			},
		},
		"a zero connect timeout is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.ConnectTimeout = 0 },
			wants:   "connect timeout must be greater than zero",
		},
		"more idle than open connections is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Pool.MaxIdle = cfg.Pool.MaxOpen + 1 },
			wants:   "max idle",
		},
		"a missing pool section is refused": {
			profile: configs.ProfileProd,
			arrange: func(cfg *configs.Database) { cfg.Pool = nil },
			wants:   "pool configuration is missing",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := serverDatabase()
			tc.arrange(cfg)

			err := cfg.Validate(tc.profile)

			if tc.wants == "" {
				if err != nil {
					t.Fatalf("expected the section to be accepted, got %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wants)
			}

			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("expected an error containing %q, got %v", tc.wants, err)
			}
		})
	}
}

func TestDevelopmentFillsInTheDatabase(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileDev
	cfg.PublicURL = "http://localhost:7500"

	cfg.Normalize()

	if cfg.Database.Driver != configs.DriverSQLite {
		t.Fatalf("expected the dev profile to select sqlite, got %q", cfg.Database.Driver)
	}

	if cfg.Database.Path == "" {
		t.Fatal("expected the dev profile to supply a database path")
	}
}

func TestProductionDoesNotGuessTheDriver(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileProd

	cfg.Normalize()

	if cfg.Database.Driver != "" {
		t.Fatalf("expected production to leave the driver undeclared, got %q", cfg.Database.Driver)
	}
}

func TestTheDriverIsCaseFolded(t *testing.T) {
	cfg := configs.Default()
	cfg.Profile = configs.ProfileProd
	cfg.Database.Driver = " POSTGRES "

	cfg.Normalize()

	if cfg.Database.Driver != configs.DriverPostgres {
		t.Fatalf("expected the driver to be folded to %q, got %q", configs.DriverPostgres, cfg.Database.Driver)
	}
}

func TestDatabaseDefaults(t *testing.T) {
	cfg := configs.Default().Database

	if cfg.ConnectTimeout != 10*time.Second {
		t.Fatalf("unexpected connect timeout %s", cfg.ConnectTimeout)
	}

	if cfg.Pool.MaxIdle != cfg.Pool.MaxOpen {
		t.Fatalf("expected the idle count to match the open count, got %d and %d", cfg.Pool.MaxIdle, cfg.Pool.MaxOpen)
	}
}
