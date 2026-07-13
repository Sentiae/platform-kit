package config

import (
	"os"
	"strconv"
	"strings"
)

// SVID-derived caller identity + per-SVID capability authz flags, read from the
// environment. Defaults are behavior-neutral so the mesh keeps today's posture
// (shared x-api-key accepted, no peer SVID required, non-strict) until each
// rollout step is switched on explicitly. These mirror [MTLSMode]'s plain-env
// getter style so services read them uniformly.

// AcceptAPIKey reports whether the legacy shared x-api-key path stays enabled,
// from APP_GRPC_ACCEPT_API_KEY. Default true (back-compat); set false to retire
// the shared token (rollout step 4).
func AcceptAPIKey() bool {
	return boolEnv("APP_GRPC_ACCEPT_API_KEY", true)
}

// RequirePeerSVID reports whether a non-skipped call with no peer SVID is
// rejected, from APP_GRPC_REQUIRE_PEER_SVID. Default false (rollout step 3).
func RequirePeerSVID() bool {
	return boolEnv("APP_GRPC_REQUIRE_PEER_SVID", false)
}

// MeshSVIDAuthzStrict reports whether SVID authz fails closed for a legacy
// api-key service caller that presents no peer SVID, from
// APP_MESH_SVID_AUTHZ_STRICT. Default false (back-compat).
func MeshSVIDAuthzStrict() bool {
	return boolEnv("APP_MESH_SVID_AUTHZ_STRICT", false)
}

// OrgEnforce reports whether the D-061 verified-org boundary enforces (vs
// shadows) org-authorization decisions, from APP_AUTH_ORG_ENFORCE. Default
// false = shadow (log divergence, do not enforce); true = enforce (the
// D-070/D-071 flip-via-boot-flag), turning a would-deny into a real error.
func OrgEnforce() bool {
	return boolEnv("APP_AUTH_ORG_ENFORCE", false)
}

// MeshServiceGrantsJSON returns the raw APP_MESH_SERVICE_GRANTS JSON override
// (empty when unset). The mesh-policy loader in tenant parses and merges it
// over the embedded default.
func MeshServiceGrantsJSON() string {
	return os.Getenv("APP_MESH_SERVICE_GRANTS")
}

// boolEnv parses a boolean env var, returning def when unset or unparseable.
func boolEnv(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
