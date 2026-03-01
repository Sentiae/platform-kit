package atlas_test

import (
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/atlas"
)

func TestGenerateHCL_DefaultEnv(t *testing.T) {
	hcl := atlas.GenerateHCL(atlas.HCLConfig{
		LoaderPath:   "./cmd/atlas-loader",
		DevURL:       "docker://postgres/16/dev?search_path=public",
		MigrationDir: "file://atlas-migrations",
	})

	checks := []string{
		`"./cmd/atlas-loader"`,
		`env "local"`,
		`docker://postgres/16/dev?search_path=public`,
		`file://atlas-migrations`,
		`external_schema`,
		`data.external_schema.gorm.url`,
	}
	for _, check := range checks {
		if !strings.Contains(hcl, check) {
			t.Errorf("expected HCL to contain %q, got:\n%s", check, hcl)
		}
	}
}

func TestGenerateHCL_CustomEnv(t *testing.T) {
	hcl := atlas.GenerateHCL(atlas.HCLConfig{
		LoaderPath:   "./loader",
		DevURL:       "docker://mysql/8/dev",
		MigrationDir: "file://migrations",
		EnvName:      "production",
	})

	if !strings.Contains(hcl, `env "production"`) {
		t.Errorf("expected custom env name 'production', got:\n%s", hcl)
	}
	if !strings.Contains(hcl, `"./loader"`) {
		t.Errorf("expected loader path './loader', got:\n%s", hcl)
	}
}

func TestGenerateHCL_FormatBlock(t *testing.T) {
	hcl := atlas.GenerateHCL(atlas.HCLConfig{
		LoaderPath:   "./cmd/atlas-loader",
		DevURL:       "docker://postgres/16/dev",
		MigrationDir: "file://migrations",
	})

	if !strings.Contains(hcl, "format") {
		t.Error("expected HCL to contain format block")
	}
	if !strings.Contains(hcl, "{{ sql .") {
		t.Error("expected HCL to contain SQL formatting directive")
	}
}
