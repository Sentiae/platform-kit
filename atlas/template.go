package atlas

import (
	"fmt"
	"strings"
)

// HCLConfig holds the values needed to render an atlas.hcl configuration file.
type HCLConfig struct {
	// LoaderPath is the Go package path for the model loader (e.g., "./cmd/atlas-loader").
	LoaderPath string
	// DevURL is the Atlas dev database URL (e.g., "docker://postgres/16/dev?search_path=public").
	DevURL string
	// MigrationDir is the migration directory (e.g., "file://atlas-migrations").
	MigrationDir string
	// EnvName is the Atlas environment name (default: "local").
	EnvName string
}

// GenerateHCL returns an atlas.hcl configuration string for the given config.
func GenerateHCL(cfg HCLConfig) string {
	envName := cfg.EnvName
	if envName == "" {
		envName = "local"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `data "external_schema" "gorm" {
  program = [
    "go",
    "run",
    %q,
  ]
}

env %q {
  src = data.external_schema.gorm.url
  dev = %q
  migration {
    dir = %q
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}
`, cfg.LoaderPath, envName, cfg.DevURL, cfg.MigrationDir)

	return b.String()
}
