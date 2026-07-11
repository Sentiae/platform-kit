package tenantdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/sentiae/platform-kit/tenant"
	"gorm.io/gorm"
)

// Posture is the tenant-isolation posture a database connection is expected to
// run under. It lets a service assert at boot that its DSN wired the RIGHT role,
// so a wrong-DSN redeploy fails LOUD instead of silently disabling RLS.
type Posture int

const (
	// PostureEnforced is the app role: NOT superuser, NOT bypassrls, so RLS
	// tenant_isolation policies apply. This is what a normal service pool wants.
	PostureEnforced Posture = iota
	// PostureBypass is the system/drain role: bypassrls, used by a future outbox
	// drainer / cross-tenant scan pool that runs tenant.WithSystemContext paths.
	PostureBypass
)

// String renders the posture for error messages.
func (p Posture) String() string {
	switch p {
	case PostureEnforced:
		return "enforced"
	case PostureBypass:
		return "bypass"
	default:
		return fmt.Sprintf("Posture(%d)", int(p))
	}
}

// ErrPostureMismatch is the fail-loud signal from AssertPosture: the current DB
// role does not match the expected tenant-isolation posture (a wrong-DSN wire).
var ErrPostureMismatch = errors.New("tenantdb: database role does not match the expected RLS posture")

// roleAttrs holds the pg_roles attributes AssertPosture checks.
type roleAttrs struct {
	RolSuper     bool `gorm:"column:rolsuper"`
	RolBypassRLS bool `gorm:"column:rolbypassrls"`
}

// AssertPosture verifies the connection's current role matches want, so a
// service can call it from DI at boot (when RLS is enabled) and refuse to start
// on a wrong DSN rather than serve every tenant's rows.
//
//   - PostureEnforced: fails if the current role is superuser OR has bypassrls
//     (either silently disables RLS), and additionally requires at least one
//     tenant_isolation policy to exist (RLS is actually installed).
//   - PostureBypass: requires the current role to HAVE bypassrls (a drain/system
//     pool that must see across tenants).
//
// Returns ErrPostureMismatch (wrapped with detail) on a mismatch; a raw error on
// a query failure.
func AssertPosture(db *gorm.DB, want Posture) error {
	// Run the posture queries on a system context so the Enforce plugin (which
	// may already be registered on this pool) skips stamping them — they are
	// system introspection (pg_roles / pg_policies), not tenant data, and carry
	// no active org, so without this they would fail closed with ErrNoActiveOrg.
	db = db.WithContext(tenant.WithSystemContext(context.Background()))

	var attrs roleAttrs
	if err := db.Raw(
		"SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user",
	).Scan(&attrs).Error; err != nil {
		return fmt.Errorf("tenantdb: read current role attributes: %w", err)
	}

	switch want {
	case PostureEnforced:
		if attrs.RolSuper || attrs.RolBypassRLS {
			return fmt.Errorf(
				"%w: want %s but current role is superuser=%t bypassrls=%t (RLS would be silently off)",
				ErrPostureMismatch, want, attrs.RolSuper, attrs.RolBypassRLS,
			)
		}
		var policies int64
		if err := db.Raw(
			"SELECT count(*) FROM pg_policies WHERE policyname = 'tenant_isolation'",
		).Scan(&policies).Error; err != nil {
			return fmt.Errorf("tenantdb: count tenant_isolation policies: %w", err)
		}
		if policies == 0 {
			return fmt.Errorf(
				"%w: want %s but no tenant_isolation policy exists (RLS not installed)",
				ErrPostureMismatch, want,
			)
		}
		return nil
	case PostureBypass:
		if !attrs.RolBypassRLS {
			return fmt.Errorf(
				"%w: want %s but current role lacks bypassrls (cannot cross tenants)",
				ErrPostureMismatch, want,
			)
		}
		return nil
	default:
		return fmt.Errorf("tenantdb: unknown posture %s", want)
	}
}
