package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeChecker struct {
	allow bool
	err   error
	calls []string
}

func (f *fakeChecker) CheckPermission(_ context.Context, subjectID, permission, rt, rid string) (bool, error) {
	f.calls = append(f.calls, subjectID+":"+permission+":"+rt+":"+rid)
	return f.allow, f.err
}

func staticExtractor(rt, id string) ResourceExtractor {
	return func(_ *http.Request) (PermissionResource, error) {
		return PermissionResource{Type: rt, ID: id}, nil
	}
}

func ctxWithSubject(subject string) context.Context {
	return context.WithValue(context.Background(), claimsCtxKey{}, Claims{Subject: subject})
}

func runMiddleware(t *testing.T, mw func(http.Handler) http.Handler, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	return rec
}

func TestRequirePermission_Allows(t *testing.T) {
	fc := &fakeChecker{allow: true}
	mw := RequirePermission(fc, "read", staticExtractor("repo", "abc"))
	rec := runMiddleware(t, mw, ctxWithSubject("user-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	if len(fc.calls) != 1 || fc.calls[0] != "user-1:read:repo:abc" {
		t.Errorf("unexpected calls: %v", fc.calls)
	}
}

func TestRequirePermission_Denies(t *testing.T) {
	fc := &fakeChecker{allow: false}
	mw := RequirePermission(fc, "write", staticExtractor("repo", "abc"))
	rec := runMiddleware(t, mw, ctxWithSubject("user-1"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
}

func TestRequirePermission_NoSubject(t *testing.T) {
	fc := &fakeChecker{allow: true}
	mw := RequirePermission(fc, "read", staticExtractor("repo", "abc"))
	rec := runMiddleware(t, mw, context.Background())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	if len(fc.calls) != 0 {
		t.Errorf("checker called without subject: %v", fc.calls)
	}
}

func TestRequirePermission_MissingResource(t *testing.T) {
	fc := &fakeChecker{allow: true}
	mw := RequirePermission(fc, "read", staticExtractor("", ""))
	rec := runMiddleware(t, mw, ctxWithSubject("user-1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
}

func TestRequirePermission_CheckerError(t *testing.T) {
	fc := &fakeChecker{err: errors.New("unreachable")}
	mw := RequirePermission(fc, "read", staticExtractor("repo", "abc"))
	rec := runMiddleware(t, mw, ctxWithSubject("user-1"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
}

func TestFixedResourceType(t *testing.T) {
	idFn := func(_ *http.Request, name string) string {
		if name == "repo_id" {
			return "r42"
		}
		return ""
	}
	ex := FixedResourceType("repository", idFn, "repo_id")
	res, err := ex(httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Type != "repository" || res.ID != "r42" {
		t.Errorf("unexpected resource: %+v", res)
	}
}
