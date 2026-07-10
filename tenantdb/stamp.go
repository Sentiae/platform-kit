// Package tenantdb stamps the per-transaction tenant GUC `app.current_org` from
// the ATTESTED caller so Postgres Row-Level Security policies (added later, per
// service) enforce tenant isolation at the database layer.
//
// The GUC is set transaction-local (set_config(..., is_local => true)), which is
// safe behind a connection pooler: it is scoped to the current transaction and
// reset on commit/rollback, so it can never leak to the next borrower of a
// pooled connection.
//
// Resolution + fail-closed policy (Stamp / StampConn):
//  1. A system context (tenant.WithSystemContext) is NOT stamped — that path
//     runs under a BYPASSRLS role. Returns nil.
//  2. The org to stamp is the requested active org (tenant.WithActiveOrg) if
//     present; else, if the principal has EXACTLY ONE authorized org, that org.
//     Otherwise fail closed with ErrNoActiveOrg (louder + safer than a silent
//     zero-row transaction).
//  3. The resolved org is re-verified against the attested principal
//     (Principal.CanActInOrg); a spoofed active org fails closed with
//     ErrOrgNotAuthorized.
//
// This package is additive: it changes no service behavior until a service
// wires Stamp/StampConn into its TransactionManager (or adopts the Enforce
// plugin — see enforce.go).
package tenantdb

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/tenant"
	"gorm.io/gorm"
)

// Sentinels. Plain errors.New (library-level): consuming services convert them
// at their handler boundary via platform-kit/errors.Register if they want a
// specific gRPC/HTTP mapping. Both are fail-closed signals — the caller MUST
// abort the transaction rather than proceed unstamped.
var (
	// ErrNoActiveOrg means no org could be resolved to stamp: neither an active
	// org was set nor did the principal have exactly one authorized org.
	ErrNoActiveOrg = errors.New("tenantdb: no active org to stamp on the transaction")
	// ErrOrgNotAuthorized means the resolved active org is not one the attested
	// principal may act in — a spoofed or stale active org.
	ErrOrgNotAuthorized = errors.New("tenantdb: principal not authorized for the active org")
)

// setConfigSQL sets the transaction-local tenant GUC. The `?` placeholder is
// translated by GORM to the driver's placeholder; the value is the org UUID
// string. is_local => true scopes it to the current transaction.
const setConfigSQL = "SELECT set_config('app.current_org', ?, true)"

// resolveOrg picks the org to stamp and re-verifies it against the attested
// principal. Callers must have already handled the system-context skip.
func resolveOrg(ctx context.Context) (uuid.UUID, error) {
	org, ok := tenant.ActiveOrgFromContext(ctx)
	if !ok {
		p, pok := tenant.FromContext(ctx)
		if !pok {
			return uuid.Nil, ErrNoActiveOrg
		}
		orgs := p.OrgIDs()
		if len(orgs) != 1 {
			return uuid.Nil, ErrNoActiveOrg
		}
		org = orgs[0]
	}
	// Re-verify: the attested principal must be able to act in the resolved org.
	// This is what makes a spoofed active-org harmless.
	p, pok := tenant.FromContext(ctx)
	if !pok || !p.CanActInOrg(org) {
		return uuid.Nil, ErrOrgNotAuthorized
	}
	return org, nil
}

// Stamp sets the tenant GUC on tx, which MUST be a live transaction (the GUC is
// transaction-local). Intended for use inside a TransactionManager's
// WithTransaction so every repository operation on that tx is tenant-scoped.
//
// System context → no stamp, nil error. Unresolvable/unauthorized org → the
// fail-closed sentinel (caller aborts the tx).
func Stamp(ctx context.Context, tx *gorm.DB) error {
	if tenant.IsSystemContext(ctx) {
		return nil
	}
	org, err := resolveOrg(ctx)
	if err != nil {
		return err
	}
	return tx.Exec(setConfigSQL, org.String()).Error
}

// StampConn sets the tenant GUC on an explicit connection a service pins for a
// unit of work (e.g. a session bound to a single connection) when it does NOT
// route through the Enforce plugin. Same resolution + fail-closed policy as
// Stamp. The transaction-local GUC only takes effect for statements that run on
// the same connection/transaction as this call, so db must be a tx or a
// connection the caller keeps for the scoped statements.
func StampConn(ctx context.Context, db *gorm.DB) error {
	if tenant.IsSystemContext(ctx) {
		return nil
	}
	org, err := resolveOrg(ctx)
	if err != nil {
		return err
	}
	return db.Exec(setConfigSQL, org.String()).Error
}
