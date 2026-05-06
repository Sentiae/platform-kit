// Package entitlement defines Sentiae's entitlement identifiers, the
// resolver interface used by services to translate a JWT entitlement-
// hash claim into a concrete entitlement set, and helpers/middleware
// to gate HTTP and gRPC endpoints by entitlement.
//
// Why a hash claim instead of an inline list:
//
// JWTs travel in every request header. Inlining 40+ entitlement
// strings makes tokens large, slows verification, and risks 8KB
// proxy header limits at scale. Instead, identity-service mints
// `ents_hash` (sha256 over the org's sorted entitlement list) into
// the JWT, and maintains a Redis-backed lookup `ents:<hash> →
// []string` so any service can resolve the set in ~1ms.
//
// Pattern of use in a downstream service:
//
//	resolver := entitlement.NewRedisResolver(rdb, identityClient)
//	mw := entitlement.RequireHTTP(resolver, entitlement.SearchSemantic)
//	router.With(mw).Get("/api/search/semantic", handler)
//
// Renamed from package `capability` 2026-05-05 (BASE.md v1.0) to
// resolve the naming clash with the catalog Capability (now
// hierarchical Feature). Identifier strings (e.g. "search.semantic")
// are unchanged; only Go types and JWT claim names changed.
package entitlement

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Entitlement is a typed identifier for a granted permission/feature
// flag. Values must match the strings stored in plans.entitlements
// JSON. Use the constants defined below to avoid typos.
type Entitlement string

func (e Entitlement) String() string { return string(e) }

// All entitlement constants. Add new ones here AND seed them into the
// appropriate plans (identity-service domain/plan_seeds.go) before
// gating any endpoint with them.
const (
	// Repository entitlements
	ReposPublicUnlimited                Entitlement = "repos.public.unlimited"
	ReposPrivateUnlimited               Entitlement = "repos.private.unlimited"
	ReposPrivateCollaboratorsMax3       Entitlement = "repos.private.collaborators_max_3"
	ReposPrivateCollaboratorsUnlimited  Entitlement = "repos.private.collaborators_unlimited"

	// Search
	SearchInverted Entitlement = "search.inverted"
	SearchSemantic Entitlement = "search.semantic"

	// Eve / LLM
	EveBasic     Entitlement = "eve.basic"
	EveFull      Entitlement = "eve.full"
	EveMaxQuota  Entitlement = "eve.max_quota"

	// Embeddings
	EmbeddingsTiered Entitlement = "embeddings.tiered"

	// VCS / patch theory
	PatchTheory      Entitlement = "patch_theory"
	SessionDAG       Entitlement = "session_dag"
	ContinuousReview Entitlement = "continuous_review"

	// Cell deployment
	CellDeploy Entitlement = "cell_deploy"

	// Marketplace
	MarketplaceInstall Entitlement = "marketplace.install"
	MarketplacePublish Entitlement = "marketplace.publish"

	// Compliance / customer health / autonomous loops
	ComplianceFrameworks Entitlement = "compliance.frameworks"
	CustomerHealth       Entitlement = "customer_health"
	GenesisEngine        Entitlement = "genesis_engine"
	SelfHealing          Entitlement = "self_healing"
	CrossRepoRefactorAI  Entitlement = "cross_repo_refactor.ai"
	TimeTravelDeep       Entitlement = "time_travel.deep"

	// Org-level
	OrgSSOSAMLOIDC Entitlement = "org.sso.saml_oidc"
	OrgAuditLog    Entitlement = "org.audit_log"
	OrgSeatBilling Entitlement = "org.seat_billing"

	// Enterprise-only
	EnterpriseFederatedRegions  Entitlement = "enterprise.federated_regions"
	EnterpriseDedicatedSupport  Entitlement = "enterprise.dedicated_support"
	EnterpriseUnlimitedQuotas   Entitlement = "enterprise.unlimited_quotas"

	// Firecracker / runtime
	FirecrackerDedicated Entitlement = "firecracker.dedicated"

	// Audit retention
	AuditLog7yr Entitlement = "audit_log.7yr"

	// Continuous review opt-ins (extra fine-grained gates)
	ContinuousReviewAuthorTest Entitlement = "continuous_review.author_test"
)

// AllKnown returns every constant defined in this package. Useful
// for admin UIs and seed scripts. Order is stable and case-folded.
func AllKnown() []Entitlement {
	return []Entitlement{
		ReposPublicUnlimited,
		ReposPrivateUnlimited,
		ReposPrivateCollaboratorsMax3,
		ReposPrivateCollaboratorsUnlimited,
		SearchInverted,
		SearchSemantic,
		EveBasic,
		EveFull,
		EveMaxQuota,
		EmbeddingsTiered,
		PatchTheory,
		SessionDAG,
		ContinuousReview,
		ContinuousReviewAuthorTest,
		CellDeploy,
		MarketplaceInstall,
		MarketplacePublish,
		ComplianceFrameworks,
		CustomerHealth,
		GenesisEngine,
		SelfHealing,
		CrossRepoRefactorAI,
		TimeTravelDeep,
		OrgSSOSAMLOIDC,
		OrgAuditLog,
		OrgSeatBilling,
		EnterpriseFederatedRegions,
		EnterpriseDedicatedSupport,
		EnterpriseUnlimitedQuotas,
		FirecrackerDedicated,
		AuditLog7yr,
	}
}

// HashSet computes the canonical sha256 hex of an entitlement set. The
// input is normalized (trim, lowercase, dedupe, sort) before hashing
// so identity-service and downstream services agree on the digest.
//
// Empty or duplicate entries are dropped silently.
func HashSet(ents []string) string {
	out := make([]string, 0, len(ents))
	seen := make(map[string]struct{}, len(ents))
	for _, e := range ents {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	sort.Strings(out)
	h := sha256.New()
	for i, e := range out {
		if i > 0 {
			_, _ = h.Write([]byte{0x1f}) // unit separator
		}
		_, _ = h.Write([]byte(e))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Set is an ordered, immutable view of entitlements granted to the
// current request's organization. Lookup is O(1) via the internal
// map. Stored on context by middleware after resolving the JWT
// ents_hash claim.
type Set struct {
	hash string
	idx  map[Entitlement]struct{}
	all  []Entitlement
}

// NewSet returns a Set built from a slice of entitlement strings.
func NewSet(ents []string) *Set {
	idx := make(map[Entitlement]struct{}, len(ents))
	all := make([]Entitlement, 0, len(ents))
	for _, e := range ents {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		ent := Entitlement(e)
		if _, exists := idx[ent]; exists {
			continue
		}
		idx[ent] = struct{}{}
		all = append(all, ent)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	rebuilt := make([]string, len(all))
	for i, e := range all {
		rebuilt[i] = string(e)
	}
	return &Set{
		hash: HashSet(rebuilt),
		idx:  idx,
		all:  all,
	}
}

// Has reports whether the set contains the given entitlement.
func (s *Set) Has(e Entitlement) bool {
	if s == nil {
		return false
	}
	_, ok := s.idx[e]
	return ok
}

// Hash returns the canonical hash of the set.
func (s *Set) Hash() string {
	if s == nil {
		return ""
	}
	return s.hash
}

// All returns a copy of the entitlement list (read-only convenience).
func (s *Set) All() []Entitlement {
	if s == nil {
		return nil
	}
	out := make([]Entitlement, len(s.all))
	copy(out, s.all)
	return out
}

// Empty reports whether the set has no entitlements.
func (s *Set) Empty() bool {
	return s == nil || len(s.all) == 0
}
