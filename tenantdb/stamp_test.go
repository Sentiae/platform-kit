package tenantdb

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/middleware"
	"github.com/sentiae/platform-kit/tenant"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var (
	orgA = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	orgB = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

// execRecord captures one built SQL statement and its bound vars.
type execRecord struct {
	sql  string
	vars []any
}

type recorder struct{ records []execRecord }

// newRecordingDB returns a DryRun sqlite DB (no real I/O) with a Raw-callback
// recorder that captures every Exec's built SQL + vars, so tests can assert the
// exact set_config call without a live Postgres.
func newRecordingDB(t *testing.T) (*gorm.DB, *recorder) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	rec := &recorder{}
	err = db.Callback().Raw().After("gorm:raw").Register("test:record", func(d *gorm.DB) {
		rec.records = append(rec.records, execRecord{
			sql:  d.Statement.SQL.String(),
			vars: append([]any(nil), d.Statement.Vars...),
		})
	})
	if err != nil {
		t.Fatalf("register recorder: %v", err)
	}
	return db, rec
}

// principalCtx returns a context carrying an explicit user Principal whose
// authorized orgs are the given scopes.
func principalCtx(ctx context.Context, orgs ...uuid.UUID) context.Context {
	scopes := make([]string, 0, len(orgs))
	for _, o := range orgs {
		scopes = append(scopes, "org:"+o.String())
	}
	return tenant.ContextWithPrincipal(ctx, tenant.Principal{
		Claims: &middleware.Claims{Subject: "user-1", Scopes: scopes},
	})
}

func TestStamp(t *testing.T) {
	tests := []struct {
		name       string
		ctx        func() context.Context
		wantErr    error
		wantStamp  bool
		wantOrgStr string
	}{
		{
			name: "active org set and authorized -> stamps that org",
			ctx: func() context.Context {
				return tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgA)
			},
			wantStamp:  true,
			wantOrgStr: orgA.String(),
		},
		{
			name: "single-org principal, no active org -> stamps that org",
			ctx: func() context.Context {
				return principalCtx(context.Background(), orgA)
			},
			wantStamp:  true,
			wantOrgStr: orgA.String(),
		},
		{
			name: "no principal, no active org -> ErrNoActiveOrg, no stamp",
			ctx: func() context.Context {
				return context.Background()
			},
			wantErr:   ErrNoActiveOrg,
			wantStamp: false,
		},
		{
			name: "multi-org principal, no active org -> ErrNoActiveOrg, no stamp",
			ctx: func() context.Context {
				return principalCtx(context.Background(), orgA, orgB)
			},
			wantErr:   ErrNoActiveOrg,
			wantStamp: false,
		},
		{
			name: "active org not among authorized orgs -> ErrOrgNotAuthorized, no stamp",
			ctx: func() context.Context {
				return tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgB)
			},
			wantErr:   ErrOrgNotAuthorized,
			wantStamp: false,
		},
		{
			name: "system context -> no stamp, nil error",
			ctx: func() context.Context {
				return tenant.WithSystemContext(tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgA))
			},
			wantStamp: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, rec := newRecordingDB(t)
			err := Stamp(tt.ctx(), db)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Stamp() err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantStamp {
				if len(rec.records) != 1 {
					t.Fatalf("want exactly 1 exec, got %d: %+v", len(rec.records), rec.records)
				}
				r := rec.records[0]
				if r.sql != setConfigSQL {
					t.Fatalf("stamp SQL = %q, want %q", r.sql, setConfigSQL)
				}
				if len(r.vars) != 1 || r.vars[0] != tt.wantOrgStr {
					t.Fatalf("stamp vars = %v, want [%s]", r.vars, tt.wantOrgStr)
				}
			} else if len(rec.records) != 0 {
				t.Fatalf("expected no exec, got %+v", rec.records)
			}
		})
	}
}

func TestStampConn(t *testing.T) {
	db, rec := newRecordingDB(t)
	ctx := tenant.WithActiveOrg(principalCtx(context.Background(), orgA), orgA)

	if err := StampConn(ctx, db); err != nil {
		t.Fatalf("StampConn() err = %v", err)
	}
	if len(rec.records) != 1 {
		t.Fatalf("want 1 exec, got %d", len(rec.records))
	}
	if rec.records[0].sql != setConfigSQL {
		t.Fatalf("SQL = %q, want %q", rec.records[0].sql, setConfigSQL)
	}
	if got := rec.records[0].vars[0]; got != orgA.String() {
		t.Fatalf("org = %v, want %s", got, orgA.String())
	}
}

func TestStampConnSystemContextSkips(t *testing.T) {
	db, rec := newRecordingDB(t)
	ctx := tenant.WithSystemContext(principalCtx(context.Background(), orgA))

	if err := StampConn(ctx, db); err != nil {
		t.Fatalf("StampConn() err = %v", err)
	}
	if len(rec.records) != 0 {
		t.Fatalf("system context must not stamp, got %+v", rec.records)
	}
}
