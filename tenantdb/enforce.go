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
// For each Query/Row/Raw/Create/Update/Delete statement that is (a) NOT already
// inside a caller-managed transaction and (b) NOT a system context, the plugin
// wraps the statement in its own transaction: it BEGINs a tx, sets the tenant
// GUC on that tx, swaps the statement's ConnPool to the tx so GORM runs the
// original statement on it, then commits (or rolls back on error) afterward.
// The resolution + fail-closed policy is identical to Stamp — an unresolvable or
// unauthorized org aborts the statement with the sentinel error.
//
// VERIFICATION STATUS: the org-resolution + fail-closed core (Stamp / StampConn)
// is unit-tested. This plugin's transaction-wrap path exercises Postgres-only
// SQL (set_config) and the database/sql tx machinery, so it is proven by an
// integration test against real Postgres (enforce_integration_test.go, build tag
// `integration`), NOT by the unit run. Do NOT enable this plugin in a service
// until that integration test passes on the server AND the service has tagged
// its system/startup/health/migration paths with tenant.WithSystemContext (they
// have no principal and would otherwise fail closed). See the task report's
// NEEDS-DECISION on enforcement strategy.
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
		{c.Row().Before("gorm:row").Register, "tenantdb:before_row", beforeStatement},
		{c.Row().After("gorm:row").Register, "tenantdb:after_row", afterStatement},
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

// beforeStatement wraps a non-transactional, non-system statement in a
// GUC-stamped transaction. It is a no-op when the statement is already inside a
// caller transaction (the caller's TxManager Stamp covers it) or is a system
// context.
func beforeStatement(db *gorm.DB) {
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if tenant.IsSystemContext(ctx) {
		return
	}
	// Already inside a caller-managed transaction? The pool is a committer; the
	// TxManager-level Stamp (or the enclosing wrap) already set the GUC. Skip.
	if _, inTx := db.Statement.ConnPool.(gorm.TxCommitter); inTx {
		return
	}
	org, err := resolveOrg(ctx)
	if err != nil {
		_ = db.AddError(err)
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
