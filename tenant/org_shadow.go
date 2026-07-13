package tenant

import (
	"context"

	"github.com/google/uuid"
	"github.com/sentiae/platform-kit/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// AuthorizeOrgShadow is the shadow/enforce counterpart of [AuthorizeOrg] for the
// D-061 verified-org boundary rollout. It runs the same FromContext +
// CanActInOrg decision once; the enforce bool decides what a would-deny does:
//
//   - enforce == false (shadow, default): a would-deny logs a structured
//     "org authz divergence" line and returns nil — no behavior change.
//   - enforce == true: a would-deny returns the real [AuthorizeOrg] error
//     (codes.Unauthenticated with no principal, else codes.PermissionDenied).
//
// An allowed call returns nil with no log. This mirrors the ship-neutral
// flip-via-boot-flag precedent ([config.OrgEnforce] gates the enforce arg).
func AuthorizeOrgShadow(ctx context.Context, org uuid.UUID, enforce bool) error {
	return orgShadow(ctx, org, enforce, codes.PermissionDenied, func() error {
		return AuthorizeOrg(ctx, org)
	})
}

// AssertOrgShadow is the shadow/enforce counterpart of [AssertOrgOrNotFound]
// for by-id reads/writes where a cross-tenant hit must not leak existence. It
// behaves exactly like [AuthorizeOrgShadow] except the enforce-branch error is
// the codes.NotFound (no existence leak) form from [AssertOrgOrNotFound].
func AssertOrgShadow(ctx context.Context, org uuid.UUID, notFoundMsg string, enforce bool) error {
	return orgShadow(ctx, org, enforce, codes.NotFound, func() error {
		return AssertOrgOrNotFound(ctx, org, notFoundMsg)
	})
}

// orgShadow computes the authz decision once and applies the shadow/enforce
// policy. denyCode is the code the would-deny attributes in the divergence log
// for a present-but-unpermitted principal; a missing principal is logged as
// codes.Unauthenticated. realErr produces the enforce-branch error (delegated
// to the real AuthorizeOrg / AssertOrgOrNotFound so codes stay identical).
func orgShadow(ctx context.Context, org uuid.UUID, enforce bool, denyCode codes.Code, realErr func() error) error {
	p, ok := FromContext(ctx)
	if ok && p.CanActInOrg(org) {
		return nil
	}
	if enforce {
		return realErr()
	}
	wouldDeny := denyCode
	if !ok {
		wouldDeny = codes.Unauthenticated
	}
	logOrgShadowDivergence(ctx, org, p, wouldDeny)
	return nil
}

// logOrgShadowDivergence emits the structured shadow-mode divergence line via
// the context logger (matching tenant/policy.go's logger.FromContext usage).
func logOrgShadowDivergence(ctx context.Context, org uuid.UUID, p Principal, wouldDeny codes.Code) {
	orgs := p.OrgIDs()
	principalOrgs := make([]string, 0, len(orgs))
	for _, id := range orgs {
		principalOrgs = append(principalOrgs, id.String())
	}
	attrs := []any{
		"derived_org", org.String(),
		"principal_orgs", principalOrgs,
		"service", p.Service,
		"service_svid", p.ServiceSVID,
		"would_deny_code", wouldDeny.String(),
	}
	if m, ok := grpc.Method(ctx); ok {
		attrs = append(attrs, "method", m)
	}
	logger.FromContext(ctx).Warn("org authz divergence", attrs...)
}
