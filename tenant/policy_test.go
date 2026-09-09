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
			"/node.v1.NodeService/ResolvePins",
			"/runtime.v1.RuntimeService/Compile",
			"/delivery.v1.DeliveryService/Build",
			"/git.v1.GitService/GetRepositoryByOwnerAndName",
			"/git.v1.GitService/CreateRepository",
			"/git.v1.GitService/GetBranch",
			"/git.v1.FileService/CommitFiles",
			"/git.v1.FileService/ReadFile",
			"/git.v1.FileService/ListFiles",
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
			"/node.v1.NodeService/ListNodes",
		},
	}
	wantCount := map[string]int{
		"spiffe://sentiae.io/svc/work":        47,
		"spiffe://sentiae.io/svc/codegen":     56,
		"spiffe://sentiae.io/svc/composition": 50,
		"spiffe://sentiae.io/svc/canvas":      54,
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

// TestVerificationIdentityGrantPinned pins the size of the D-226
// verification-identity grant: one runtime read method, the three RPCs the
// node-as-repository Phase 1 acceptance drive invokes with the ephemeral
// svc/verify SVID, delivery's RunFlow (Phase 4), and the three RPCs Phase 5's
// drive adds (codegen Scaffold + CompileFlow, git DeleteRepository). The grant
// is resident in the embedded default (not a birth-time env override) because
// .245-class hosts receive env exactly once, at image birth; this test is what
// keeps that resident record from widening.
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
			if len(gr.Methods) != 8 {
				t.Fatalf("grant has %d methods, want exactly 8 (%v)", len(gr.Methods), sortedKeys(gr.Methods))
			}
			if _, ok := gr.Methods[granted]; !ok {
				t.Fatalf("grant's methods are %v, want %q among them", sortedKeys(gr.Methods), granted)
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

// TestNodeRegistryGrantPinned pins the node-as-repository Phase 1 mesh grant:
// svc/node acts cross-org over exactly the eight git-service RPCs its git
// gateway invokes (GRANT-WHAT-YOU-CALL, D-223) and nothing else. Control:
// delete "/git.v1.FileService/GetArchive" from nodeRegistryGrants → red.
func TestNodeRegistryGrantPinned(t *testing.T) {
	const svid = "spiffe://sentiae.io/svc/node"
	granted := []string{
		"/git.v1.GitService/GetRepositoryByOwnerAndName",
		"/git.v1.GitService/CreateRepository",
		"/git.v1.GitService/CreateTag",
		"/git.v1.GitService/GetTag",
		"/git.v1.FileService/CommitFiles",
		"/git.v1.FileService/ReadFile",
		"/git.v1.FileService/ListFiles",
		"/git.v1.FileService/GetArchive",
	}

	// LoadMeshPolicy merges APP_MESH_SERVICE_GRANTS over the embedded table, so an
	// ambient value would decide this test instead of the code under test.
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
			if len(gr.Methods) != len(granted) {
				t.Fatalf("grant has %d methods, want exactly %d (%v)", len(gr.Methods), len(granted), sortedKeys(gr.Methods))
			}
			for _, m := range granted {
				if !grants.AllowsMethod(svid, m) {
					t.Errorf("%q must allow %q", svid, m)
				}
			}
			if grants.AllowsMethod(svid, "/git.v1.GitService/DeleteRepository") {
				t.Errorf("%q must NOT allow %q", svid, "/git.v1.GitService/DeleteRepository")
			}
		})
	}
}

// TestVerificationIdentityGrantPinned_Phase1 pins the three RPCs the Phase 1
// acceptance drive invokes with the ephemeral svc/verify SVID, and the denials
// that are the premise of the drive's deny probes: svc/verify holds no
// GetTag/CreateTag grant, and node-service's InstallNode is not granted.
// Control: delete "/git.v1.FileService/GetArchive" from
// verificationIdentityGrants → red.
func TestVerificationIdentityGrantPinned_Phase1(t *testing.T) {
	const svid = "spiffe://sentiae.io/svc/verify"
	granted := []string{
		"/runtime.v1.ResourceProvisioning/GetResourceStatus",
		"/node.v1.NodeService/RegisterNodeRepository",
		"/git.v1.GitService/CreateRepository",
		"/git.v1.FileService/GetArchive",
	}
	denied := []string{
		"/node.v1.NodeService/InstallNode",
		"/git.v1.GitService/GetTag",
		"/git.v1.GitService/CreateTag",
	}

	for name, grants := range map[string]ServiceGrants{
		"default": DefaultMeshPolicy(),
		"loaded":  func() ServiceGrants { t.Setenv("APP_MESH_SERVICE_GRANTS", ""); return LoadMeshPolicy() }(),
	} {
		t.Run(name, func(t *testing.T) {
			for _, m := range granted {
				if !grants.AllowsMethod(svid, m) {
					t.Errorf("%q must allow %q", svid, m)
				}
			}
			for _, m := range denied {
				if grants.AllowsMethod(svid, m) {
					t.Errorf("%q must NOT allow %q", svid, m)
				}
			}
		})
	}
}

// TestRegistryGrant_ResolveOnly pins the node-as-repository Phase 2 (L-2) mesh
// grant: svc/registry acts cross-org over exactly ONE identity RPC — the
// org-slug resolution its OCI push authorization performs (GRANT-WHAT-YOU-CALL,
// D-223) — and over nothing else, and it is never a member of the blanket
// cross-org TCB. Control: delete the "spiffe://sentiae.io/svc/registry" entry
// from registryGrants in policy.go → this test is red.
func TestRegistryGrant_ResolveOnly(t *testing.T) {
	const (
		svid    = "spiffe://sentiae.io/svc/registry"
		granted = "/identity.v1.OrganizationService/GetOrganizationBySlug"
	)
	denied := []string{
		"/identity.v1.OrganizationService/GetOrganization",
		"/identity.v1.OrganizationService/ListOrganizations",
		"/identity.v1.OrganizationService/CreateOrganization",
		"",
	}

	for _, blanket := range crossOrgMeshServices {
		if blanket == svid {
			t.Fatalf("%q must NOT be in the blanket cross-org list", svid)
		}
	}

	// LoadMeshPolicy merges APP_MESH_SERVICE_GRANTS over the embedded table, so an
	// ambient value would decide this test instead of the code under test.
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
			if !grants.AllowsOrg(svid, orgA) {
				t.Fatalf("%q must be allowed to act cross-org", svid)
			}
			if len(gr.Methods) != 1 {
				t.Fatalf("grant has %d methods, want exactly 1 (%v)", len(gr.Methods), sortedKeys(gr.Methods))
			}
			if !grants.AllowsMethod(svid, granted) {
				t.Fatalf("%q must allow %q", svid, granted)
			}
			for _, m := range denied {
				if grants.AllowsMethod(svid, m) {
					t.Errorf("%q must NOT allow %q", svid, m)
				}
			}
		})
	}
}

// TestFlowBuildGrantsPinned_Phase5 names, one RPC at a time, the four grants the
// node-as-repository Phase 5 flow build adds — so that dropping any ONE of them
// fails with that method printed, rather than only as an arithmetic count.
//
// codegen calls delivery's Build at DESIGN §4.2 step 9 (the compiled monolith's
// image), and the acceptance drive's ephemeral svc/verify identity scaffolds the
// component, compiles its flow in mode=build, and disposes of the node
// repository through git-service's own delete path. GRANT-WHAT-YOU-CALL (D-223):
// each denial below is a real sibling RPC on a service already reached, never
// called on these paths.
//
// CONTROL (one per grant): delete that method from policy.go's grant list and
// this test names it — "must allow …".
func TestFlowBuildGrantsPinned_Phase5(t *testing.T) {
	const (
		codegen = "spiffe://sentiae.io/svc/codegen"
		verify  = "spiffe://sentiae.io/svc/verify"
	)
	cases := []struct {
		svid    string
		granted []string
		denied  []string
	}{
		{
			svid:    codegen,
			granted: []string{"/delivery.v1.DeliveryService/Build"},
			denied: []string{
				"/delivery.v1.DeliveryService/Deploy",
				"/delivery.v1.DeliveryService/Release",
				"/delivery.v1.DeliveryService/Retarget",
			},
		},
		{
			svid: verify,
			granted: []string{
				"/codegen.v1.CodegenService/Scaffold",
				"/codegen.v1.CodegenService/CompileFlow",
				"/git.v1.GitService/DeleteRepository",
			},
			denied: []string{
				"/codegen.v1.CodegenService/Eject",
				"/codegen.v1.CodegenService/TransitionAuthorship",
				"/git.v1.GitService/DeleteBranch",
			},
		},
	}

	// LoadMeshPolicy merges APP_MESH_SERVICE_GRANTS over the embedded table, so an
	// ambient value would decide this test instead of the code under test.
	for name, grants := range map[string]ServiceGrants{
		"default": DefaultMeshPolicy(),
		"loaded":  func() ServiceGrants { t.Setenv("APP_MESH_SERVICE_GRANTS", ""); return LoadMeshPolicy() }(),
	} {
		t.Run(name, func(t *testing.T) {
			for _, c := range cases {
				for _, m := range c.granted {
					if !grants.AllowsMethod(c.svid, m) {
						t.Errorf("%q must allow %q", c.svid, m)
					}
				}
				for _, m := range c.denied {
					if grants.AllowsMethod(c.svid, m) {
						t.Errorf("%q must NOT allow %q", c.svid, m)
					}
				}
			}
		})
	}
}

// TestCodegenGitGrantsPinned_D412 pins the six git RPCs codegen's git gateway
// invokes on the Scaffold / TransitionAuthorship / CompileFlow paths, and pins
// the sibling git RPCs it does NOT call as denied (GRANT-WHAT-YOU-CALL, D-223).
//
// It drives the REAL enforcement path — Principal.CanActInOrg, which is what
// git-service's inbound propagation check calls — not ServiceGrants.AllowsMethod
// alone. That matters because CanActInOrg returns true at principal.go:139 for
// ANY peer SVID while meshSVIDAuthzStrict is false, which is the package default
// in unit tests: a version of this test that forgot to set strict mode would
// pass on every method, granted or not, and prove nothing. Strict is therefore
// set explicitly and restored via t.Cleanup.
//
// CONTROL (one per grant): delete a method from policy.go's codegen entry and
// this test names it — "must allow …".
func TestCodegenGitGrantsPinned_D412(t *testing.T) {
	const codegen = "spiffe://sentiae.io/svc/codegen"

	granted := []string{
		"/git.v1.GitService/GetRepositoryByOwnerAndName",
		"/git.v1.GitService/CreateRepository",
		"/git.v1.GitService/GetBranch",
		"/git.v1.FileService/CommitFiles",
		"/git.v1.FileService/ReadFile",
		"/git.v1.FileService/ListFiles",
	}
	// Sibling git RPCs codegen's code never invokes.
	denied := []string{
		"/git.v1.GitService/DeleteRepository",
		"/git.v1.GitService/CreateTag",
		"/git.v1.GitService/DeleteBranch",
		"/git.v1.FileService/GetArchive",
	}

	// LoadMeshPolicy merges APP_MESH_SERVICE_GRANTS over the embedded table, so an
	// ambient value would decide this test instead of the code under test.
	for name, grants := range map[string]ServiceGrants{
		"default": DefaultMeshPolicy(),
		"loaded":  func() ServiceGrants { t.Setenv("APP_MESH_SERVICE_GRANTS", ""); return LoadMeshPolicy() }(),
	} {
		t.Run(name, func(t *testing.T) {
			prevGrants := defaultServiceGrants
			SetServiceGrants(grants)
			t.Cleanup(func() { SetServiceGrants(prevGrants) })

			prevStrict := meshSVIDAuthzStrict
			SetMeshSVIDAuthzStrict(true)
			t.Cleanup(func() { SetMeshSVIDAuthzStrict(prevStrict) })

			// Headless caller: no user claims, exactly as codegen->git runs.
			call := func(method string) bool {
				return Principal{ServiceSVID: codegen, Method: method}.CanActInOrg(orgA)
			}
			for _, m := range granted {
				if !call(m) {
					t.Errorf("%q must allow %q", codegen, m)
				}
			}
			for _, m := range denied {
				if call(m) {
					t.Errorf("%q must NOT allow %q", codegen, m)
				}
			}
		})
	}
}

// TestGrantSourcesDisjoint guards the silent-skip hazard in the grant merge
// (D-412 residual, entailed per D-295). Every add*Grants helper skips an SVID
// already present in the map — `if _, exists := m[svid]; exists { continue }` —
// so a second source naming an SVID a earlier source already populated
// contributes NOTHING, with every existing test still green. That is a guard
// that cannot fail unless it is written: this is it.
//
// CONTROL: add any SVID below to a second source and this test names both.
func TestGrantSourcesDisjoint(t *testing.T) {
	// Merge order in DefaultMeshPolicy / LoadMeshPolicy. A later source is the
	// one silently skipped, so the order is part of what is being pinned.
	sources := []struct {
		name  string
		svids []string
	}{
		{"crossOrgMeshServices", crossOrgMeshServices},
		{"methodScopedCatalogReaders", sortedMapKeys(methodScopedCatalogReaders)},
		{"verificationIdentityGrants", sortedMapKeys(verificationIdentityGrants)},
		{"nodeRegistryGrants", sortedMapKeys(nodeRegistryGrants)},
		{"registryGrants", sortedMapKeys(registryGrants)},
	}

	owner := make(map[string]string)
	for _, src := range sources {
		for _, svid := range src.svids {
			if prev, dup := owner[svid]; dup {
				t.Errorf("%q is populated by %q and again by %q; the later source is "+
					"silently skipped by the add*Grants exists-check and grants nothing",
					svid, prev, src.name)
				continue
			}
			owner[svid] = src.name
		}
	}
}

// sortedMapKeys returns the keys of a grant source map in a deterministic order
// so a duplicate is always reported against the same owning source.
func sortedMapKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
