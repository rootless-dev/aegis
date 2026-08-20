package configbuilder

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/rootless-dev/aegis/internal/configs"
	"github.com/rootless-dev/aegis/internal/infra/envtools"
)

// DefaultConfigPaths are searched in order when no explicit path is given. A
// missing file is not an error: outside development the orchestrator injects
// the settings through the environment instead.
var DefaultConfigPaths = []string{
	"aegis.yaml",
	"/etc/aegis/aegis.yaml",
}

// ConfigPathEnvVar names the variable that overrides the search entirely. When
// it is set, the file has to exist: an explicit path that silently does
// nothing is worse than a failed boot.
const ConfigPathEnvVar = "AEGIS_CONFIG_FILE"

// ProfileEnvVar is how an orchestrator selects the execution profile, where
// passing a command line flag to a container is far more awkward than setting a
// variable.
const ProfileEnvVar = "AEGIS_PROFILE"

var ErrConfigInstanceNotInitialized = errors.New("configbuilder: no source was loaded, call WithDefaults before validating or building")

type ConfigBuilder struct {
	cfg *configs.Application
	err error
}

func New() *ConfigBuilder {
	return &ConfigBuilder{}
}

// WithDefaults installs the base layer. Every other source writes over it.
func (b *ConfigBuilder) WithDefaults() *ConfigBuilder {
	b.cfg = configs.Default()

	return b
}

// WithYAML applies a configuration file over whatever is loaded, overwriting
// only the keys the file declares.
func (b *ConfigBuilder) WithYAML() *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	if path, ok := envtools.Lookup[string](ConfigPathEnvVar); ok {
		if err := loadYAML(path, b.cfg); err != nil {
			b.err = errors.Join(b.err, fmt.Errorf("configbuilder: %s=%q: %w", ConfigPathEnvVar, path, err))
		}

		return b
	}

	for _, path := range DefaultConfigPaths {
		err := loadYAML(path, b.cfg)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			b.err = errors.Join(b.err, fmt.Errorf("configbuilder: %w", err))
		}

		// The first file found wins; the remaining paths are fallbacks, not
		// layers to merge.
		return b
	}

	return b
}

// WithEnv applies the environment over whatever is loaded. It comes after the
// file because the file is what ships with the image, while a variable is how a
// single instance is adjusted without rebuilding anything.
func (b *ConfigBuilder) WithEnv() *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	applyEnv(b.cfg)

	return b
}

// WithFlags parses and applies the command line, which comes last because it is
// the most specific source there is: whoever typed it is looking at the process
// right now.
func (b *ConfigBuilder) WithFlags(args []string) *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	applyFlags(b.cfg, parseFlags(args))

	return b
}

// Normalize resolves what depends on the profile, and therefore can only be
// settled once every source has had its say.
func (b *ConfigBuilder) Normalize() *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	b.cfg.Normalize()

	return b
}

func (b *ConfigBuilder) Validate() *ConfigBuilder {
	if b.cfg == nil {
		b.err = errors.Join(b.err, ErrConfigInstanceNotInitialized)

		return b
	}

	b.err = errors.Join(b.err, b.cfg.Validate())

	return b
}

// Build reports every problem found along the chain at once, so a misconfigured
// boot does not have to be fixed one setting per run.
func (b *ConfigBuilder) Build() (*configs.Application, error) {
	if b.err != nil {
		return nil, b.err
	}

	if b.cfg == nil {
		return nil, ErrConfigInstanceNotInitialized
	}

	return b.cfg, nil
}
