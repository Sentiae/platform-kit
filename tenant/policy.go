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
