package tenant

import (
	"context"
	"encoding/json"

	"github.com/sentiae/platform-kit/config"
	"github.com/sentiae/platform-kit/logger"
)

// crossOrgMeshServices are the platform services trusted to act cross-org on a
// user's behalf — the cross-org trusted computing base (TCB). Keys are full
// SPIFFE IDs; every other service is deny-by-default. This is the single
// embedded source of truth, overridable via APP_MESH_SERVICE_GRANTS.
var crossOrgMeshServices = []string{
	"spiffe://sentiae.io/svc/foundry",
	"spiffe://sentiae.io/svc/delivery",
	"spiffe://sentiae.io/svc/runtime",
	"spiffe://sentiae.io/svc/catalog",
	"spiffe://sentiae.io/svc/conversation",
	"spiffe://sentiae.io/svc/notification",
	"spiffe://sentiae.io/svc/knowledge",
	"spiffe://sentiae.io/svc/ops",
	"spiffe://sentiae.io/svc/augur",
	"spiffe://sentiae.io/svc/vigil",
	"spiffe://sentiae.io/svc/bff",
}

// catalogReadMethods are the non-mutating (Get/List/Resolve/Validate) gRPC
// full-methods across catalog-service. They are the only cross-org methods the
// method-scoped catalog readers (see methodScopedCatalogReaders, D-072) may
// call: readers get cross-org rights ONLY to read the shared system model, never
// to mutate it. Mutating RPCs — including the state-changing Resolve*Violation
// RPCs — are deliberately excluded.
var catalogReadMethods = []string{
	// ComponentBodyService
	"/catalog.v1.ComponentBodyService/GetBody",
	// ArchitectureRulesService
	"/catalog.v1.ArchitectureRulesService/GetRuleSet",
	"/catalog.v1.ArchitectureRulesService/ListRuleSets",
	"/catalog.v1.ArchitectureRulesService/GetResolvedRuleSet",
	"/catalog.v1.ArchitectureRulesService/ValidateEdge",
	"/catalog.v1.ArchitectureRulesService/ValidateGraph",
	// ArchitectureGraphService
	"/catalog.v1.ArchitectureGraphService/GetArchNode",
	"/catalog.v1.ArchitectureGraphService/ListArchNodesByGraph",
	"/catalog.v1.ArchitectureGraphService/ListArchNodesByComponent",
	"/catalog.v1.ArchitectureGraphService/GetArchEdge",
	"/catalog.v1.ArchitectureGraphService/ListArchEdgesByGraph",
	"/catalog.v1.ArchitectureGraphService/GetArchGraph",
	// EngineeringCatalogService
	"/catalog.v1.EngineeringCatalogService/GetDomainEntity",
	"/catalog.v1.EngineeringCatalogService/ListDomainEntitiesByComponent",
	"/catalog.v1.EngineeringCatalogService/GetDatabaseTable",
	"/catalog.v1.EngineeringCatalogService/ListDatabaseTablesByComponent",
	"/catalog.v1.EngineeringCatalogService/GetAPIContract",
	"/catalog.v1.EngineeringCatalogService/ListAPIContractsByComponent",
	"/catalog.v1.EngineeringCatalogService/GetKafkaTopic",
	"/catalog.v1.EngineeringCatalogService/ListKafkaTopicsByProducer",
	"/catalog.v1.EngineeringCatalogService/ListKafkaTopicsByConsumer",
	"/catalog.v1.EngineeringCatalogService/ListTopicConsumers",
	"/catalog.v1.EngineeringCatalogService/GetComponentDeclaredDep",
	"/catalog.v1.EngineeringCatalogService/ListComponentDeclaredDepsBySource",
	"/catalog.v1.EngineeringCatalogService/ListComponentDeclaredDepsByTarget",
	"/catalog.v1.EngineeringCatalogService/GetArchConstraint",
	"/catalog.v1.EngineeringCatalogService/ListArchConstraints",
	"/catalog.v1.EngineeringCatalogService/ListViolationsByComponent",
	"/catalog.v1.EngineeringCatalogService/ListViolationsByConstraint",
	"/catalog.v1.EngineeringCatalogService/ListRecentDetectorRuns",
	"/catalog.v1.EngineeringCatalogService/GetPlanFlag",
	"/catalog.v1.EngineeringCatalogService/GetPlanFlagBySlug",
	"/catalog.v1.EngineeringCatalogService/ListPlanFlags",
	"/catalog.v1.EngineeringCatalogService/GetADR",
	"/catalog.v1.EngineeringCatalogService/GetADRByNumber",
	"/catalog.v1.EngineeringCatalogService/ListADRs",
	"/catalog.v1.EngineeringCatalogService/ListADRsByComponent",
	// EnvironmentService
	"/catalog.v1.EnvironmentService/GetEnvironment",
	"/catalog.v1.EnvironmentService/ListEnvironments",
	// ComponentCatalogService
	"/catalog.v1.ComponentCatalogService/ListComponents",
	"/catalog.v1.ComponentCatalogService/GetComponent",
	"/catalog.v1.ComponentCatalogService/GetComponentBySlug",
	// DeploymentTargetService
	"/catalog.v1.DeploymentTargetService/GetDeploymentTarget",
	"/catalog.v1.DeploymentTargetService/ListDeploymentTargets",
	// OwnershipService
	"/catalog.v1.OwnershipService/ListOwners",
	"/catalog.v1.OwnershipService/ListOwnedBy",
	"/catalog.v1.OwnershipService/ResolveOwner",
}

// methodScopedCatalogReaders (D-072) are SVIDs that call catalog but are NOT in
// the blanket cross-org TCB. Each is granted cross-org rights restricted to
// catalogReadMethods only — they may act cross-org solely to READ catalog's
// shared system model, never to mutate it and never to reach any other service.
var methodScopedCatalogReaders = map[string][]string{
	"spiffe://sentiae.io/svc/work":        catalogReadMethods,
	"spiffe://sentiae.io/svc/codegen":     catalogReadMethods,
	"spiffe://sentiae.io/svc/composition": catalogReadMethods,
	"spiffe://sentiae.io/svc/canvas":      catalogReadMethods,
}

// addMethodScopedCatalogReaders merges the D-072 method-scoped catalog-read
// grants into m. It does not overwrite an entry already present (blanket TCB
// entries win — none of the four readers is in that list today).
func addMethodScopedCatalogReaders(m map[string]ServiceGrant) {
	for svid, methods := range methodScopedCatalogReaders {
		if _, exists := m[svid]; exists {
			continue
		}
		set := make(map[string]struct{}, len(methods))
		for _, meth := range methods {
			set[meth] = struct{}{}
		}
		m[svid] = ServiceGrant{CrossOrg: true, Methods: set}
	}
}

// meshGrantOverride is the JSON shape of one entry in APP_MESH_SERVICE_GRANTS.
type meshGrantOverride struct {
	CrossOrg bool     `json:"cross_org"`
	Methods  []string `json:"methods"`
}

// DefaultMeshPolicy returns the embedded ratifiable default: the cross-org TCB
// services granted CrossOrg with no per-method restriction. Every other service
// has no grant (deny-by-default).
func DefaultMeshPolicy() ServiceGrants {
	m := make(map[string]ServiceGrant, len(crossOrgMeshServices))
	for _, svid := range crossOrgMeshServices {
		m[svid] = ServiceGrant{CrossOrg: true}
	}
	addMethodScopedCatalogReaders(m)
	return NewServiceGrants(m)
}

// LoadMeshPolicy returns DefaultMeshPolicy merged with the APP_MESH_SERVICE_GRANTS
// override (env wins per SVID). The override JSON shape is:
//
//	{"spiffe://sentiae.io/svc/foo":{"cross_org":true,"methods":["/pkg.Svc/M"]}}
//
// Malformed JSON is logged and ignored (fail-safe): the embedded default is
// returned rather than crashing the process.
func LoadMeshPolicy() ServiceGrants {
	m := make(map[string]ServiceGrant, len(crossOrgMeshServices))
	for _, svid := range crossOrgMeshServices {
		m[svid] = ServiceGrant{CrossOrg: true}
	}
	// D-072 method-scoped catalog readers are merged BEFORE the env override so
	// an APP_MESH_SERVICE_GRANTS entry can still adjust any of them.
	addMethodScopedCatalogReaders(m)

	raw := config.MeshServiceGrantsJSON()
	if raw == "" {
		return NewServiceGrants(m)
	}

	var overrides map[string]meshGrantOverride
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		logger.FromContext(context.Background()).Warn(
			"malformed APP_MESH_SERVICE_GRANTS; using default mesh policy", "error", err)
		return NewServiceGrants(m)
	}

	for svid, ov := range overrides {
		if svid == "" {
			continue
		}
		gr := ServiceGrant{CrossOrg: ov.CrossOrg}
		if len(ov.Methods) > 0 {
			gr.Methods = make(map[string]struct{}, len(ov.Methods))
			for _, meth := range ov.Methods {
				if meth != "" {
					gr.Methods[meth] = struct{}{}
				}
			}
		}
		m[svid] = gr
	}
	return NewServiceGrants(m)
}
