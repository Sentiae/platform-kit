//go:build integration

package tenantdb

import (
	"context"
	"errors"
	"testing"

	"github.com/sentiae/platform-kit/tenant"
	"github.com/sentiae/platform-kit/testutil"
	"gorm.io/gorm"
)

// pinSingleConn forces the pool to one physical connection so a "no bleed" check
// is a real same-connection assertion (the next statement provably reuses the
// connection ReadScoped/the plugin just released).
func pinSingleConn(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
}

// TestEnforcePluginCursorReads proves the beforeRowStatement contract against
// real Postgres: a bare cursor read (Raw().Scan) fails closed with
// ErrCursorOutsideTx, the same read wrapped in ReadScoped succeeds with the GUC
// visible inside, and the transaction-local GUC does NOT bleed onto the pooled
// connection afterward. Postgres-only (set_config/current_setting + the tx
// machinery are exercised for real). Run: go test -tags=integration ./tenantdb/...
func TestEnforcePluginCursorReads(t *testing.T) {
	db := testutil.NewTestDB(t, "")
	pinSingleConn(t, db)
	if err := db.Use(Enforce()); err != nil {
		t.Fatalf("use plugin: %v", err)
	}

	ctx := tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgA)

	// (c) A bare cursor read (Row processor) on the pool must fail closed — the
	// plugin cannot self-wrap it without force-closing the cursor on Commit.
	var dead string
	err := db.WithContext(ctx).Raw("SELECT current_setting('app.current_org', true)").Scan(&dead).Error
	if !errors.Is(err, ErrCursorOutsideTx) {
		t.Fatalf("bare cursor read: err = %v, want ErrCursorOutsideTx", err)
	}

	// (d) The same read wrapped in ReadScoped succeeds; the GUC is the resolved org
	// inside the scoped transaction.
	var inside string
	if err := ReadScoped(ctx, db, func(tx *gorm.DB) error {
		return tx.Raw("SELECT current_setting('app.current_org', true)").Scan(&inside).Error
	}); err != nil {
		t.Fatalf("ReadScoped read: %v", err)
	}
	if inside != orgA.String() {
		t.Fatalf("in-scope app.current_org = %q, want %q", inside, orgA.String())
	}

	// (d, no bleed) On the SAME pooled connection (MaxOpenConns=1), a subsequent
	// system-context read sees an EMPTY GUC — ReadScoped's tx-local stamp was reset
	// on commit and cannot ride the connection into the next borrower.
	sysCtx := tenant.WithSystemContext(context.Background())
	var after string
	if err := db.WithContext(sysCtx).Raw("SELECT current_setting('app.current_org', true)").Scan(&after).Error; err != nil {
		t.Fatalf("system read after ReadScoped: %v", err)
	}
	if after != "" {
		t.Fatalf("GUC bled onto the pooled connection: got %q, want empty", after)
	}
}

// TestEnforcePluginQueryFailClosed proves the Query path (Find) self-wraps and
// fails closed: a read with no resolvable org aborts with ErrNoActiveOrg, and a
// spoofed active org (a principal for orgA asking to act in orgB) aborts with
// ErrOrgNotAuthorized. Neither ever runs unstamped.
func TestEnforcePluginQueryFailClosed(t *testing.T) {
	db := testutil.NewTestDB(t, "")
	// Create the table BEFORE installing the plugin — once Enforce is active a bare
	// DDL Exec with no org context fail-closes (which is exactly why services tag
	// their migration/DDL paths with tenant.WithSystemContext).
	if err := db.Exec("CREATE TABLE t_fc (organization_id uuid)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Use(Enforce()); err != nil {
		t.Fatalf("use plugin: %v", err)
	}

	// (b) No org resolvable (no principal, no active org, no system org).
	var rows []fcRow
	err := db.WithContext(context.Background()).Find(&rows).Error
	if !errors.Is(err, ErrNoActiveOrg) {
		t.Fatalf("no-org Find: err = %v, want ErrNoActiveOrg", err)
	}

	// (g) Spoofed active org: principal is authorized for orgA, requests orgB.
	badCtx := tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgB)
	err = db.WithContext(badCtx).Find(&rows).Error
	if !errors.Is(err, ErrOrgNotAuthorized) {
		t.Fatalf("spoofed active-org Find: err = %v, want ErrOrgNotAuthorized", err)
	}
}

type fcRow struct {
	OrganizationID string `gorm:"column:organization_id"`
}

func (fcRow) TableName() string { return "t_fc" }

// TestEnforcePluginRestampsUnsanctionedTx proves the plugin re-stamps a statement
// running inside a repository's OWN GORM .Transaction(...) — a tx the TxManager
// Stamp never covered — using a process-internal system-org ctx (a kafka consumer
// / per-org job). A cursor read INSIDE that tx is safe (the caller's tx outlives
// the cursor), so the GUC reflects the system org; a NON-system ctx with NO org
// resolvable must fail closed.
func TestEnforcePluginRestampsUnsanctionedTx(t *testing.T) {
	db := testutil.NewTestDB(t, "")
	if err := db.Use(Enforce()); err != nil {
		t.Fatalf("use plugin: %v", err)
	}

	// A repo opens its own transaction outside WithTransaction (no WithStamped
	// marker). Under a system-org ctx the plugin re-stamps each statement on the
	// tx, so the GUC inside reflects the system org — and the cursor read is safe
	// because it lives inside the caller's transaction.
	sysOrgCtx := tenant.WithSystemOrg(context.Background(), orgA)
	err := db.WithContext(sysOrgCtx).Transaction(func(tx *gorm.DB) error {
		var got string
		if err := tx.Raw("SELECT current_setting('app.current_org', true)").Scan(&got).Error; err != nil {
			return err
		}
		if got != orgA.String() {
			t.Fatalf("in-tx app.current_org = %q, want %q", got, orgA.String())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unsanctioned tx under system-org: %v", err)
	}

	// No org resolvable (no principal, no active org, no system org) inside a
	// self-opened tx must fail closed (ErrNoActiveOrg), never run unstamped.
	err = db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		var x string
		return tx.Raw("SELECT current_setting('app.current_org', true)").Scan(&x).Error
	})
	if err == nil {
		t.Fatal("expected fail-closed error when no org is resolvable in an unsanctioned tx")
	}
}
