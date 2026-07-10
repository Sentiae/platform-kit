package tenant

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestWithActiveOrgRoundTrip(t *testing.T) {
	org := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	ctx := WithActiveOrg(context.Background(), org)

	got, ok := ActiveOrgFromContext(ctx)
	if !ok {
		t.Fatal("expected active org present")
	}
	if got != org {
		t.Fatalf("got %s, want %s", got, org)
	}
}

func TestActiveOrgFromContextAbsent(t *testing.T) {
	if _, ok := ActiveOrgFromContext(context.Background()); ok {
		t.Fatal("expected no active org on a bare context")
	}
}

func TestSystemContextRoundTrip(t *testing.T) {
	if IsSystemContext(context.Background()) {
		t.Fatal("bare context must not be a system context")
	}
	ctx := WithSystemContext(context.Background())
	if !IsSystemContext(ctx) {
		t.Fatal("expected system context after WithSystemContext")
	}
}

func TestSystemAndActiveOrgIndependent(t *testing.T) {
	org := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	ctx := WithSystemContext(WithActiveOrg(context.Background(), org))

	if !IsSystemContext(ctx) {
		t.Fatal("expected system context")
	}
	got, ok := ActiveOrgFromContext(ctx)
	if !ok || got != org {
		t.Fatalf("active org lost: got %s ok=%v", got, ok)
	}
}
