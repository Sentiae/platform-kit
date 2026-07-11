package tenantdb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sentiae/platform-kit/tenant"
	"gorm.io/gorm"
)

// errBeginUnsupported is returned when the statement's connection pool cannot
// begin a transaction — the Enforce plugin cannot safely stamp a bare
// (non-transactional) statement without wrapping it in a transaction, so it
// fails closed rather than run the statement unstamped.
var errBeginUnsupported = errors.New("tenantdb: connection pool does not support transactions; cannot enforce tenant GUC")

// pgSetConfigSQL is the raw-driver form of the tenant GUC set. The Enforce
// plugin runs it directly on a *sql.Tx (bypassing GORM's placeholder rewrite),
// so it uses the Postgres positional placeholder $1. RLS is a Postgres-only
// mechanism, so coupling here is acceptable. The value (a UUID string) is passed
// as a bound parameter — never interpolated (root §30.17).
const pgSetConfigSQL = "SELECT set_config('app.current_org', $1, true)"

// txHolderSettingKey stores the wrapping transaction across the Before/After
// callback pair for a single statement.
const txHolderSettingKey = "tenantdb:tx_holder"

// stampedCtxKey marks a ctx whose transaction the sanctioned path already
// stamped (a TxManager.WithTransaction that called Stamp). The Enforce plugin
// skips such statements so it does not re-stamp what is already covered. Typed
// key per root §18.
type stampedCtxKey struct{}

// WithStamped marks ctx as already tenant-stamped by the sanctioned path. A
// TxManager.WithTransaction calls this right after its explicit Stamp so the
// Enforce plugin skips the statements it runs on that transaction (they are
// already GUC-scoped). Statements on an UNMARKED transaction — a repository that
// opened its own GORM .Transaction(...) outside WithTransaction — are NOT
// covered by this and get re-stamped per statement by the plugin.
func WithStamped(ctx context.Context) context.Context {
	return context.WithValue(ctx, stampedCtxKey{}, true)
}

// isStamped reports whether ctx was marked by WithStamped.
func isStamped(ctx context.Context) bool {
	v, ok := ctx.Value(stampedCtxKey{}).(bool)
	return ok && v
}

// committerPool is a connection pool that is also a transaction committer — the
// shape returned by both a *sql.Tx and GORM's prepared-statement tx pool.
type committerPool interface {
	gorm.ConnPool
	gorm.TxCommitter
}

// txHolder carries the wrapping transaction and the pool to restore after the
// statement completes.
type txHolder struct {
	tx      committerPool
	restore gorm.ConnPool
}

// Enforce returns a GORM plugin that closes the NON-transactional statement hole
// left by TxManager-level stamping: a bare `db.WithContext(ctx).First(...)` runs
// outside any caller transaction, where a transaction-local GUC set separately
// would evaporate (and may even land on a different pooled connection).
//
// For each Query/Row/Raw/Create/Update/Delete statement that is NOT a system
// context and NOT already sanctioned-stamped (WithStamped), the plugin stamps
// the tenant GUC: if the statement runs OUTSIDE any transaction it wraps it in
// its own GUC-stamped transaction (BEGIN, set the GUC, swap ConnPool so GORM
// runs the statement on it, then commit/rollback); if it runs inside an
// UNSANCTIONED transaction (a repository's own GORM .Transaction(...) that the
// TxManager Stamp never covered) it re-execs set_config directly on that tx
// (transaction-local + idempotent). Statements on a WithStamped ctx are skipped
// because the sanctioned WithTransaction already stamped them. The resolution +
// fail-closed policy is identical to Stamp — an unresolvable or unauthorized org
// aborts the statement with the sentinel error.
//
// Cursor reads (Raw().Scan / Row() / Rows()) are handled separately by
// beforeRowStatement, which never self-wraps (it would force-close the caller's
// cursor on Commit): it re-stamps an existing transaction or fails closed with
// ErrCursorOutsideTx. Wrap such reads in tenantdb.ReadScoped or the service's
// TransactionManager.
//
// VERIFICATION STATUS: the org-resolution + fail-closed core (resolveOrg, shared
// with Stamp) is unit-tested. This plugin's transaction-wrap path exercises
// Postgres-only SQL (set_config) and the database/sql tx machinery, so it is
// proven by an integration test against real Postgres (enforce_integration_test.go,
// build tag `integration`), NOT by the unit run. Do NOT enable this plugin in a
// service until that integration test passes on the server AND the service has
// tagged its system/startup/health/migration paths with tenant.WithSystemContext
// (they have no principal and would otherwise fail closed) AND its cursor reads
// are wrapped (grep Raw().Scan / Row() / Rows()).
func Enforce() gorm.Plugin { return enforcePlugin{} }

type enforcePlugin struct{}

func (enforcePlugin) Name() string { return "tenantdb:enforce" }

func (enforcePlugin) Initialize(db *gorm.DB) error {
	c := db.Callback()
	regs := []struct {
		register func(name string, fn func(*gorm.DB)) error
		name     string
		fn       func(*gorm.DB)
	}{
		{c.Query().Before("gorm:query").Register, "tenantdb:before_query", beforeStatement},
		{c.Query().After("gorm:query").Register, "tenantdb:after_query", afterStatement},
		// Row processor (Raw().Scan / Row() / Rows()) uses a DEDICATED handler and
		// registers NO after-callback: GORM hands the caller the still-open cursor
		// AFTER this chain returns, so a self-wrapping tx (whose after-callback would
		// Commit here) would force-close the rows before they are read. See
		// beforeRowStatement — it never self-wraps; it either re-stamps an existing
		// tx or fails closed (ErrCursorOutsideTx).
		{c.Row().Before("gorm:row").Register, "tenantdb:before_row", beforeRowStatement},
		{c.Raw().Before("gorm:raw").Register, "tenantdb:before_raw", beforeStatement},
		{c.Raw().After("gorm:raw").Register, "tenantdb:after_raw", afterStatement},
		{c.Create().Before("gorm:create").Register, "tenantdb:before_create", beforeStatement},
		{c.Create().After("gorm:create").Register, "tenantdb:after_create", afterStatement},
		{c.Update().Before("gorm:update").Register, "tenantdb:before_update", beforeStatement},
		{c.Update().After("gorm:update").Register, "tenantdb:after_update", afterStatement},
		{c.Delete().Before("gorm:delete").Register, "tenantdb:before_delete", beforeStatement},
		{c.Delete().After("gorm:delete").Register, "tenantdb:after_delete", afterStatement},
	}
	for _, r := range regs {
		if err := r.register(r.name, r.fn); err != nil {
			return err
		}
	}
	return nil
}

// beforeStatement stamps the tenant GUC for a non-system statement.
//
//   - System context (tenant.WithSystemContext) → skip (BYPASSRLS path).
//   - Sanctioned-stamped ctx (WithStamped) → skip: a TxManager.WithTransaction
//     already ran Stamp on this transaction, so the plugin must not re-stamp it.
//   - Already inside a transaction but NOT marked stamped (a repository that
//     opened its own GORM .Transaction(...) outside WithTransaction) → re-exec
//     set_config directly on that tx and return; the GUC is transaction-local, so
//     this per-statement re-set is idempotent and tx-scoped. Do NOT self-wrap —
//     we are already in a tx.
//   - Not in a transaction (a bare db.WithContext(ctx).First(...)) → self-wrap in
//     a GUC-stamped transaction (the original behavior).
//
// Resolution + fail-closed policy is identical to Stamp — an unresolvable or
// unauthorized org aborts the statement via db.AddError.
func beforeStatement(db *gorm.DB) {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if tenant.IsSystemContext(ctx) {
		return
	}
	// The sanctioned path (TxManager.WithTransaction) already stamped this tx.
	if isStamped(ctx) {
		return
	}
	org, err := resolveOrg(ctx)
	if err != nil {
		_ = db.AddError(err)
		return
	}
	// Inside an unsanctioned transaction (a repo's own .Transaction(...)): the
	// TxManager-level Stamp never ran, so re-set the tenant GUC directly on the
	// tx. Transaction-local + idempotent — safe to repeat per statement.
	if pool, inTx := db.Statement.ConnPool.(committerPool); inTx {
		if _, err := pool.ExecContext(ctx, pgSetConfigSQL, org.String()); err != nil {
			_ = db.AddError(err)
		}
		return
	}
	tx, err := beginTx(ctx, db.Statement.ConnPool)
	if err != nil {
		_ = db.AddError(err)
		return
	}
	if _, err := tx.ExecContext(ctx, pgSetConfigSQL, org.String()); err != nil {
		_ = tx.Rollback()
		_ = db.AddError(err)
		return
	}
	db.Set(txHolderSettingKey, &txHolder{tx: tx, restore: db.Statement.ConnPool})
	db.Statement.ConnPool = tx
}

// beforeRowStatement stamps the tenant GUC for a cursor-style read — the Row
// processor backs Raw().Scan(), Row() and Rows(), where GORM hands the caller a
// live *sql.Rows/*sql.Row consumed AFTER this callback chain returns. It must
// therefore NEVER open a self-wrapping transaction: a wrapping tx would have to
// Commit in an after-callback, and database/sql's Tx.Commit force-closes any
// still-open rows first, so the cursor would die on read with "context
// cancelled". Instead:
//
//   - System context / already sanctioned-stamped (WithStamped) → skip, exactly
//     like beforeStatement.
//   - Inside a live transaction (the caller wrapped the read in
//     tenantdb.ReadScoped, a TxManager.WithTransaction, or a repo's own
//     .Transaction(...)) → re-exec set_config directly on that tx. The caller's
//     transaction outlives the cursor, so this is safe, transaction-local, and
//     idempotent. (WithStamped already short-circuits the ReadScoped/TxManager
//     case; this covers an unsanctioned repo tx.)
//   - Bare pool, no surrounding transaction → FAIL CLOSED with ErrCursorOutsideTx.
//     The read must be wrapped (tenantdb.ReadScoped or the TransactionManager) so
//     the cursor lives inside a stamped transaction. Failing loudly at the exact
//     call site beats returning a cursor that dies on first read.
func beforeRowStatement(db *gorm.DB) {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if tenant.IsSystemContext(ctx) {
		return
	}
	if isStamped(ctx) {
		return
	}
	pool, inTx := db.Statement.ConnPool.(committerPool)
	if !inTx {
		_ = db.AddError(ErrCursorOutsideTx)
		return
	}
	org, err := resolveOrg(ctx)
	if err != nil {
		_ = db.AddError(err)
		return
	}
	if _, err := pool.ExecContext(ctx, pgSetConfigSQL, org.String()); err != nil {
		_ = db.AddError(err)
	}
}

// afterStatement commits (or rolls back on error) the wrapping transaction and
// restores the original pool. A no-op when beforeStatement did not wrap.
func afterStatement(db *gorm.DB) {
	v, ok := db.Get(txHolderSettingKey)
	if !ok {
		return
	}
	holder, ok := v.(*txHolder)
	if !ok {
		return
	}
	db.Statement.ConnPool = holder.restore
	if db.Error != nil {
		_ = holder.tx.Rollback()
		return
	}
	if err := holder.tx.Commit(); err != nil {
		_ = db.AddError(err)
	}
}

// beginTx opens a transaction from pool, returning a pool that is also a
// committer. Handles both a plain *sql.DB (TxBeginner → *sql.Tx) and GORM's
// prepared-statement pool (ConnPoolBeginner → a committer ConnPool).
func beginTx(ctx context.Context, pool gorm.ConnPool) (committerPool, error) {
	switch b := pool.(type) {
	case gorm.ConnPoolBeginner:
		cp, err := b.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			return nil, err
		}
		tx, ok := cp.(committerPool)
		if !ok {
			return nil, errBeginUnsupported
		}
		return tx, nil
	case gorm.TxBeginner:
		tx, err := b.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			return nil, err
		}
		return tx, nil
	default:
		return nil, errBeginUnsupported
	}
}
