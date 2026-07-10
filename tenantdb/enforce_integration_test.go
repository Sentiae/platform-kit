//go:build integration

package tenantdb

import (
	"context"
	"testing"

	"github.com/sentiae/platform-kit/tenant"
	"github.com/sentiae/platform-kit/testutil"
	"gorm.io/gorm"
)

// TestEnforcePluginStampsNonTxStatement proves the Enforce plugin wraps a bare
// (non-transactional) statement in a GUC-stamped transaction against real
// Postgres: after a plain read, current_setting('app.current_org') reflects the
// resolved org, and a system context leaves it unset. Postgres-only because
// set_config / current_setting and the tx machinery are exercised for real.
//
// Run: go test -tags=integration ./tenantdb/... (needs Docker for testcontainers).
func TestEnforcePluginStampsNonTxStatement(t *testing.T) {
	db := testutil.NewTestDB(t, "")
	if err := db.Use(Enforce()); err != nil {
		t.Fatalf("use plugin: %v", err)
	}

	// Authorized read: the plugin must set app.current_org for the statement.
	ctx := tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgA)
	var got string
	// A raw read that returns the GUC observed on the same wrapped transaction.
	if err := db.WithContext(ctx).Raw("SELECT current_setting('app.current_org', true)").Scan(&got).Error; err != nil {
		t.Fatalf("read GUC: %v", err)
	}
	if got != orgA.String() {
		t.Fatalf("app.current_org = %q, want %q", got, orgA.String())
	}

	// Unauthorized active org: the statement must fail closed.
	badCtx := tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgB)
	var x string
	err := db.WithContext(badCtx).Raw("SELECT current_setting('app.current_org', true)").Scan(&x).Error
	if err == nil {
		t.Fatal("expected fail-closed error for unauthorized active org")
	}

	// System context: no stamp; the GUC stays empty.
	sysCtx := tenant.WithSystemContext(context.Background())
	var sys string
	if err := db.WithContext(sysCtx).Raw("SELECT current_setting('app.current_org', true)").Scan(&sys).Error; err != nil {
		t.Fatalf("system read: %v", err)
	}
	if sys != "" {
		t.Fatalf("system context must not stamp; got %q", sys)
	}
}

// TestEnforcePluginRestampsUnsanctionedTx proves the Enforce plugin re-stamps a
// statement running inside a repository's OWN GORM .Transaction(...) — a tx the
// TxManager Stamp never covered — using a process-internal system-org ctx (a
// kafka consumer / per-org job). Inside the transaction the GUC must reflect the
// system org; a NON-system ctx with NO org resolvable must fail closed.
//
// Run: go test -tags=integration ./tenantdb/... (needs Docker for testcontainers).
func TestEnforcePluginRestampsUnsanctionedTx(t *testing.T) {
	db := testutil.NewTestDB(t, "")
	if err := db.Use(Enforce()); err != nil {
		t.Fatalf("use plugin: %v", err)
	}

	// A repo opens its own transaction outside WithTransaction (no WithStamped
	// marker). Under a system-org ctx the plugin re-stamps each statement, so the
	// GUC inside the tx reflects the system org.
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
