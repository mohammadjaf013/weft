package cli

import (
	"os"

	"gopkg.in/yaml.v3"
)

// writeYAML marshals a config to disk.
func writeYAML(path string, c any) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
