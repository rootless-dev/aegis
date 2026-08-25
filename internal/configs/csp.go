package configs

// CSP declares whether the Content-Security-Policy header is sent. The policy
// itself is not configurable, for the same reason the TLS cipher suites are
// not: one pinned in a file ages into the weakest thing this service still
// allows, and an operator able to weaken it is a downgrade attack.
type CSP struct {
	Enabled bool `yaml:"enabled"`
}

func defaultCSP() *CSP {
	return &CSP{
		Enabled: true,
	}
}

func (cfg *CSP) Validate() error {
	return nil
}
