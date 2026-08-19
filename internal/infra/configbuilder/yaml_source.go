package configbuilder

import (
	"bytes"
	"fmt"
	"os"

	"github.com/rootless-dev/aegis/internal/configs"
	"gopkg.in/yaml.v3"
)

// loadYAML decodes the file straight over cfg, which already carries the
// defaults. Keys the document does not mention keep whatever is there, so the
// file only overwrites what it actually declares.
func loadYAML(path string, cfg *configs.Application) error {
	// The path comes from AEGIS_CONFIG_FILE or from the fixed search list, both
	// of them boot configuration rather than request input. Whoever sets that
	// variable already controls the process, so there is no traversal to
	// escalate through, and scoping this under os.Root would only stop an
	// operator from pointing at a file of their choosing.
	content, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(content))
	// A misspelled key becomes an error rather than a setting that quietly does
	// nothing.
	decoder.KnownFields(true)

	if err := decoder.Decode(cfg); err != nil {
		return fmt.Errorf("parsing %q: %w", path, err)
	}

	return nil
}
