// Package tenantdb stamps the per-transaction tenant GUC `app.current_org` from
// the ATTESTED caller so Postgres Row-Level Security policies (added later, per
// service) enforce tenant isolation at the database layer.
//
// The GUC is set transaction-local (set_config(..., is_local => true)), which is
// safe behind a connection pooler: it is scoped to the current transaction and
// reset on commit/rollback, so it can never leak to the next borrower of a
// pooled connection.
//
// Resolution + fail-closed policy (Stamp / ReadScoped / the Enforce plugin all
// share resolveOrg):
//  1. A system context (tenant.WithSystemContext) is NOT stamped — that path
//     runs under a BYPASSRLS role. Returns nil.
//  2. A system-org (tenant.WithSystemOrg) — highest precedence — is stamped WITH
//     NO principal re-verify: a kafka consumer or per-org job loop has no
//     Principal, so the trust anchor is the process's own subscription/job, not a
//     caller assertion. A nil system-org fails closed with ErrNilSystemOrg.
//  3. Else the org to stamp is the requested active org (tenant.WithActiveOrg) if
//     present; else, if the principal has EXACTLY ONE authorized org, that org.
//     Otherwise fail closed with ErrNoActiveOrg (louder + safer than a silent
//     zero-row transaction).
//  4. That resolved org (active-org / single-org paths) is re-verified against
//     the attested principal (Principal.CanActInOrg); a spoofed active org fails
//     closed with ErrOrgNotAuthorized.
//
// This package is additive: it changes no service behavior until a service
// wires Stamp into its TransactionManager (or adopts the Enforce plugin for the
// read path — see enforce.go — plus ReadScoped for cursor reads).
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
	// ErrNilSystemOrg means a system-org context (tenant.WithSystemOrg) carried
	// uuid.Nil — a resolution bug in the consumer/job (e.g. an empty envelope org)
	// that must fail closed rather than stamp a zero org.
	ErrNilSystemOrg = errors.New("tenantdb: system-org context carried a nil org")
	// ErrCursorOutsideTx means a cursor-style read (Raw().Scan / Row() / Rows())
	// on a tenant table ran on a bare pool with no surrounding transaction. The
	// Enforce plugin CANNOT safely stamp such a statement: GORM consumes the cursor
	// AFTER the callback chain returns, but a plugin-opened wrapping tx would have
	// to commit inside that chain — and database/sql's Tx.Commit force-closes the
	// still-open rows first ("context canceled"). So the plugin fails closed here
	// instead of returning a cursor that dies on read. Wrap the read in
	// tenantdb.ReadScoped (or the service's TransactionManager) so the cursor lives
	// inside a stamped transaction that outlives it.
	ErrCursorOutsideTx = errors.New("tenantdb: cursor read (Raw/Row/Rows) on a tenant table requires a transaction; use tenantdb.ReadScoped or the TransactionManager")
)

// setConfigSQL sets the transaction-local tenant GUC. The `?` placeholder is
// translated by GORM to the driver's placeholder; the value is the org UUID
// string. is_local => true scopes it to the current transaction.
const setConfigSQL = "SELECT set_config('app.current_org', ?, true)"

// resolveOrg picks the org to stamp. Callers must have already handled the
// system-context skip. Precedence:
//  1. A system-org (tenant.WithSystemOrg) — highest — is stamped WITH NO
//     principal re-verify: a consumer/job ctx has no Principal; the trust anchor
//     is the process's own subscription/job. A nil system-org fails closed with
//     ErrNilSystemOrg.
//  2. Else the requested active org (tenant.WithActiveOrg), re-verified against
//     the attested principal (fail closed ErrOrgNotAuthorized on a spoof).
//  3. Else a principal with EXACTLY ONE authorized org, re-verified.
//  4. Else ErrNoActiveOrg.
func resolveOrg(ctx context.Context) (uuid.UUID, error) {
	// (1) Process-internal system actor scoped to one org (consumer/job). No
	// principal to verify — the trust anchor is the process, not a caller.
	if sysOrg, ok := tenant.SystemOrgFromContext(ctx); ok {
		if sysOrg == uuid.Nil {
			return uuid.Nil, ErrNilSystemOrg
		}
		return sysOrg, nil
	}
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

// ReadScoped runs fn inside a single tenant-stamped transaction — the sanctioned
// way to execute a cursor-style read (Raw().Scan / Row() / Rows()) or any bare
// read that must be tenant-scoped without going through a service's write-path
// TransactionManager. It BEGINs a transaction, stamps the tenant GUC on it (same
// resolution + fail-closed policy as Stamp; a system context runs the tx WITHOUT
// a stamp, under the BYPASSRLS role), marks the ctx WithStamped so the Enforce
// plugin does not re-stamp the inner statements, runs fn on that transaction, and
// commits (or rolls back on error). Because the cursor now lives inside a tx that
// outlives it, the ErrCursorOutsideTx hazard cannot occur.
//
// Read-only by convention. Writes must keep using the service TransactionManager
// (so the outbox write rides the same tx, root §19) — ReadScoped is for reads.
func ReadScoped(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := Stamp(ctx, tx); err != nil {
			return err
		}
		return fn(tx.WithContext(WithStamped(ctx)))
	})
}
