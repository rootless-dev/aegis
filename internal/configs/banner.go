package configs

type Banner struct {
	// Enabled prints the startup banner. Worth turning off where logs are
	// collected and parsed as structured entries: the art is not JSON, and a
	// collector may report the lines as malformed.
	Enabled bool `yaml:"enabled"`
}

func (cfg *Banner) Validate() error {
	return nil
}
