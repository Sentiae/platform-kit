// Package atlas provides helpers for Atlas database migration tooling.
// It wraps atlas-provider-gorm to standardize how services expose their
// GORM models for schema diffing and migration generation.
package atlas

import (
	"fmt"
	"io"
	"os"

	"ariga.io/atlas-provider-gorm/gormschema"
)

// LoadAndPrint loads GORM models and prints the DDL schema to stdout.
// This is the standard entry point for a service's cmd/atlas-loader/main.go.
//
// Example:
//
//	func main() {
//	    atlas.LoadAndPrint("postgres", &domain.User{}, &domain.Organization{})
//	}
func LoadAndPrint(dialect string, models ...any) {
	if err := LoadAndWrite(os.Stdout, dialect, models...); err != nil {
		fmt.Fprintf(os.Stderr, "atlas loader: %v\n", err)
		os.Exit(1)
	}
}

// LoadAndWrite loads GORM models and writes the DDL schema to the given writer.
func LoadAndWrite(w io.Writer, dialect string, models ...any) error {
	stmts, err := gormschema.New(dialect).Load(models...)
	if err != nil {
		return fmt.Errorf("failed to load GORM models: %w", err)
	}
	_, err = io.WriteString(w, stmts)
	return err
}
