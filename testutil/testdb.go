// Package testutil provides shared test infrastructure helpers for integration tests.
//
// Each helper starts a temporary container via testcontainers-go and registers
// cleanup via t.Cleanup(). All helpers are safe for parallel use across subtests.
//
// Example — PostgreSQL with GORM:
//
//	func TestRepository(t *testing.T) {
//	    db := testutil.NewTestDB(t, "path/to/atlas-migrations")
//	    repo := postgres.NewUserRepo(db)
//	    // ... run assertions
//	}
package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewTestDB starts a temporary PostgreSQL container, optionally runs Atlas
// migrations from migrationsDir, and returns a *gorm.DB. The container is
// terminated via t.Cleanup.
//
// If migrationsDir is empty, no migrations are applied (you can AutoMigrate
// your GORM models instead).
//
// Example:
//
//	db := testutil.NewTestDB(t, "../../atlas-migrations")
//	// or without migrations:
//	db := testutil.NewTestDB(t, "")
func NewTestDB(t *testing.T, migrationsDir string) *gorm.DB {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("testutil.NewTestDB: start postgres container: %v", err)
	}

	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("testutil.NewTestDB: terminate postgres container: %v", err)
		}
	})

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("testutil.NewTestDB: get connection string: %v", err)
	}

	// Run Atlas migrations if a directory was provided.
	if migrationsDir != "" {
		absDir, err := filepath.Abs(migrationsDir)
		if err != nil {
			t.Fatalf("testutil.NewTestDB: resolve migrations dir: %v", err)
		}
		if _, err := os.Stat(absDir); os.IsNotExist(err) {
			t.Fatalf("testutil.NewTestDB: migrations dir does not exist: %s", absDir)
		}
		applyAtlasMigrations(t, connStr, absDir)
	}

	db, err := gorm.Open(pgdriver.Open(connStr), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("testutil.NewTestDB: open gorm connection: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("testutil.NewTestDB: get sql.DB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB.Close()
	})

	return db
}

// applyAtlasMigrations runs atlas migrate apply against the given database.
func applyAtlasMigrations(t *testing.T, connStr, migrationsDir string) {
	t.Helper()

	cmd := exec.Command("atlas", "migrate", "apply",
		"--dir", fmt.Sprintf("file://%s", migrationsDir),
		"--url", connStr,
	)
	cmd.Env = append(os.Environ(), "ATLAS_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("testutil.NewTestDB: atlas migrate apply: %v\n%s", err, out)
	}
}
