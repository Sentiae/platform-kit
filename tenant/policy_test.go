package tenant

import (
	"sort"
	"strings"
	"testing"
)

// TestMethodScopedCatalogReaders confirms D-072: the four catalog-reader SVIDs
// get cross-org rights restricted to catalog's read RPCs (a mutating catalog RPC
// is denied), while a blanket TCB service keeps unrestricted cross-org.
func TestMethodScopedCatalogReaders(t *testing.T) {
	const (
		work    = "spiffe://sentiae.io/svc/work"
		foundry = "spiffe://sentiae.io/svc/foundry"
		read    = "/catalog.v1.ComponentCatalogService/GetComponent"
		write   = "/catalog.v1.ComponentCatalogService/CreateComponent"
	)

	for _, policy := range []struct {
		name string
		g    ServiceGrants
	}{
		{"default", DefaultMeshPolicy()},
		{"load", func() ServiceGrants { t.Setenv("APP_MESH_SERVICE_GRANTS", ""); return LoadMeshPolicy() }()},
	} {
		t.Run(policy.name, func(t *testing.T) {
			g := policy.g

			// work is a method-scoped catalog reader.
			if !g.AllowsOrg(work, orgA) {
				t.Fatalf("work SVID must have CrossOrg")
			}
			if !g.AllowsMethod(work, read) {
				t.Fatalf("work SVID must allow catalog read %q", read)
			}
			if g.AllowsMethod(work, write) {
				t.Fatalf("work SVID must NOT allow catalog mutation %q", write)
			}

			// foundry is a blanket TCB service: cross-org, no method restriction.
			if !g.AllowsOrg(foundry, orgA) {
				t.Fatalf("foundry SVID must have CrossOrg")
			}
			if !g.AllowsMethod(foundry, write) {
				t.Fatalf("blanket foundry SVID must allow any method (empty Methods)")
			}
		})
	}
}

// TestCatalogReadMethodsSnapshot pins the shared read base as one approved
// capability atom: exactly 47 distinct catalog read full-methods, no mutation.
// Widening or narrowing it is a deliberate act that must edit this number.
func TestCatalogReadMethodsSnapshot(t *testing.T) {
	const want = 47
	if got := len(catalogReadMethods); got != want {
		t.Fatalf("catalogReadMethods has %d entries, want exactly %d", got, want)
	}
	seen := make(map[string]struct{}, want)
	for _, m := range catalogReadMethods {
		if _, dup := seen[m]; dup {
			t.Fatalf("duplicate entry %q", m)
		}
		seen[m] = struct{}{}
		if !strings.HasPrefix(m, "/catalog.v1.") {
			t.Fatalf("%q is not a catalog-service method", m)
		}
	}
}

// TestMethodScopedReaderGrantDrift is the platform-kit half of the D-223
// bidirectional superset guard: each restricted SVID's effective grant must
// equal its declared set exactly, so both an addition and a removal fail here.
//
// The other half — deriving each caller's outbound generated-gRPC invocations by
// go/types inventory across the service repos and comparing them against these
// declarations — cannot run inside platform-kit (it has no view of the callers)
// and is owned by #service-grants-methods-never-constrain-crossorg.
func TestMethodScopedReaderGrantDrift(t *testing.T) {
	// The audited extras per restricted caller (D-223 §1.3): the RPCs that
	// SVID's code actually invokes beyond the catalog read base.
	expected := map[string][]string{
		"spiffe://sentiae.io/svc/work": nil,
		"spiffe://sentiae.io/svc/codegen": {
			"/runtime.v1.RuntimeService/Compile",
		},
		"spiffe://sentiae.io/svc/composition": {
			"/catalog.v1.ComponentBodyService/UpsertBodySnapshot",
			"/work.v1.WorkBodyService/GetBody",
			"/work.v1.WorkBodyService/UpsertBodySnapshot",
		},
		"spiffe://sentiae.io/svc/canvas": {
			"/runtime.v1.GraphService/CreateGraph",
			"/runtime.v1.GraphService/DeployGraph",
			"/runtime.v1.GraphService/ExecuteGraph",
			"/runtime.v1.GraphService/GetGraphExecution",
			"/runtime.v1.GraphService/CancelGraphExecution",
			"/runtime.v1.GraphService/ListNodeExecutions",
		},
	}
	wantCount := map[string]int{
		"spiffe://sentiae.io/svc/work":        47,
		"spiffe://sentiae.io/svc/codegen":     48,
		"spiffe://sentiae.io/svc/composition": 50,
		"spiffe://sentiae.io/svc/canvas":      53,
	}

	g := DefaultMeshPolicy()

	if len(methodScopedCatalogReaders) != len(expected) {
		t.Fatalf("restricted reader count = %d, want %d (an SVID was added or removed)",
			len(methodScopedCatalogReaders), len(expected))
	}
	for svid := range methodScopedCatalogReaders {
		if _, ok := expected[svid]; !ok {
			t.Fatalf("%q has a grant but no declared expected set", svid)
		}
	}

	for svid, extras := range expected {
		t.Run(svid, func(t *testing.T) {
			gr, ok := g.byID[svid]
			if !ok {
				t.Fatalf("%q has no grant", svid)
			}
			if !gr.CrossOrg {
				t.Fatalf("%q must have CrossOrg", svid)
			}
			want := make(map[string]struct{}, len(catalogReadMethods)+len(extras))
			for _, m := range catalogReadMethods {
				want[m] = struct{}{}
			}
			for _, m := range extras {
				want[m] = struct{}{}
			}
			if len(gr.Methods) != wantCount[svid] {
				t.Fatalf("grant has %d methods, want %d", len(gr.Methods), wantCount[svid])
			}
			// Granted-but-undeclared (a silent widening).
			for _, m := range sortedKeys(gr.Methods) {
				if _, ok := want[m]; !ok {
					t.Errorf("granted but not declared: %q", m)
				}
			}
			// Declared-but-ungranted (a silent narrowing that breaks a caller).
			for _, m := range sortedKeys(want) {
				if _, ok := gr.Methods[m]; !ok {
					t.Errorf("declared but not granted: %q", m)
				}
			}
		})
	}
}

// TestVerificationIdentityGrantPinned pins the D-226 verification-identity grant
// at exactly one read method. The grant is resident in the embedded default (not
// a birth-time env override) because .245-class hosts receive env exactly once,
// at image birth; this test is what keeps that resident record from widening.
func TestVerificationIdentityGrantPinned(t *testing.T) {
	const svid = "spiffe://sentiae.io/svc/verify"
	const granted = "/runtime.v1.ResourceProvisioning/GetResourceStatus"
	// LoadMeshPolicy merges APP_MESH_SERVICE_GRANTS over the embedded table, so an
	// ambient value would decide this test instead of the code under test. Cleared
	// exactly as TestMethodScopedCatalogReaders does above.
	for name, grants := range map[string]ServiceGrants{
		"default": DefaultMeshPolicy(),
		"loaded":  func() ServiceGrants { t.Setenv("APP_MESH_SERVICE_GRANTS", ""); return LoadMeshPolicy() }(),
	} {
		t.Run(name, func(t *testing.T) {
			gr, ok := grants.byID[svid]
			if !ok {
				t.Fatalf("%q has no grant", svid)
			}
			if !gr.CrossOrg {
				t.Fatalf("%q must have CrossOrg", svid)
			}
			if len(gr.Methods) != 1 {
				t.Fatalf("grant has %d methods, want exactly 1 (%v)", len(gr.Methods), sortedKeys(gr.Methods))
			}
			if _, ok := gr.Methods[granted]; !ok {
				t.Fatalf("grant's single method is %v, want %q", sortedKeys(gr.Methods), granted)
			}
			if !grants.AllowsMethod(svid, granted) {
				t.Fatalf("%q must allow %q", svid, granted)
			}
			for _, denied := range []string{
				"/runtime.v1.ResourceProvisioning/ProvisionResource",
				"/runtime.v1.ResourceProvisioning/DecommissionResource",
				"",
			} {
				if grants.AllowsMethod(svid, denied) {
					t.Fatalf("%q must NOT allow %q", svid, denied)
				}
			}
		})
	}
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMethodScopedReadersNotBlanket guards against a regression where a reader
// leaks into the unrestricted TCB list.
func TestMethodScopedReadersNotBlanket(t *testing.T) {
	blanket := make(map[string]struct{}, len(crossOrgMeshServices))
	for _, svid := range crossOrgMeshServices {
		blanket[svid] = struct{}{}
	}
	for svid := range methodScopedCatalogReaders {
		if _, ok := blanket[svid]; ok {
			t.Fatalf("%q must NOT be in the blanket cross-org list", svid)
		}
	}
}
