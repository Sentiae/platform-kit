package tenant

import "testing"

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
