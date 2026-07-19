package tenant

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/sentiae/platform-kit/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Wire-metadata keys carrying tenant/caller identity out-of-band on a gRPC call.
// They mirror the ad-hoc keys services already append by hand (delivery's
// org_metadata_interceptor, the BFF gateway); the propagation interceptors below
// make them uniform across the mesh so org survives every hop.
const (
	MDOrganizationID = "x-organization-id"
	MDUserID         = "x-user-id"
	MDRequestID      = "x-request-id"
	MDAgentID        = "x-agent-id"
	MDApproverID     = "x-approver-id"
)

// agentIDCtxKey / approverIDCtxKey carry the pass-through agent + approver
// attribution across a hop. They are opaque strings — this layer never
// interprets them, it only relays them so a downstream service that DOES care
// (an approval audit) sees the same value the edge set. Typed keys per root §18.
type agentIDCtxKey struct{}
type approverIDCtxKey struct{}

// propagatedUserCtxKey holds a user id that arrived over the wire (x-user-id) on
// a call whose principal is a SERVICE (no verified JWT). It is the multi-hop
// carrier of "on behalf of whom" for headless service-to-service calls; it is
// NEVER populated from a call that carries verified Claims (the verified subject
// wins and is never overridden). Typed key per root §18.
type propagatedUserCtxKey struct{}

// WithAgentID stashes the pass-through agent id on ctx.
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDCtxKey{}, id)
}

// AgentIDFromContext reads the pass-through agent id set by WithAgentID or the
// inbound interceptor.
func AgentIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(agentIDCtxKey{}).(string)
	return v, ok && v != ""
}

// WithApproverID stashes the pass-through approver id on ctx.
func WithApproverID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, approverIDCtxKey{}, id)
}

// ApproverIDFromContext reads the pass-through approver id set by WithApproverID
// or the inbound interceptor.
func ApproverIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(approverIDCtxKey{}).(string)
	return v, ok && v != ""
}

// PropagatedUserID returns the user id relayed over the wire on a service-
// principal call (see propagatedUserCtxKey). ok is false when none was relayed
// or when the call carried verified Claims (which are never overridden).
func PropagatedUserID(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(propagatedUserCtxKey{}).(uuid.UUID)
	return v, ok
}

// EffectiveOrg resolves the single organization this ctx should act in, for
// propagation onto an outbound call. Order:
//
//  1. ActiveOrgFromContext — a request whose caller was authorized for org.
//  2. SystemOrgFromContext — a process-internal actor scoped to one org (a
//     consumer stamping the event's org, one pass of a per-org job loop).
//  3. A Principal that authorizes EXACTLY ONE org — unambiguous, so it is the
//     org the caller is acting in.
//
// It does NOT consult IsSystemContext: a BYPASSRLS system path must never ride
// an org onto the wire, and the outbound interceptor short-circuits that case
// before calling here. ok is false when no org is unambiguously resolvable —
// the caller then propagates nothing (never refuses; the callee's tenantdb
// fails closed, and the client cannot know whether the callee is org-scoped).
func EffectiveOrg(ctx context.Context) (uuid.UUID, bool) {
	if org, ok := ActiveOrgFromContext(ctx); ok {
		return org, true
	}
	if org, ok := SystemOrgFromContext(ctx); ok {
		return org, true
	}
	if p, ok := FromContext(ctx); ok {
		if ids := p.OrgIDs(); len(ids) == 1 {
			return ids[0], true
		}
	}
	return uuid.Nil, false
}

// clientOrgPropagationTotal counts outbound calls by what happened to the org
// header: propagated (this interceptor filled it), inherited (already present,
// left untouched), absent (nothing resolvable), system (a BYPASSRLS path — org
// deliberately withheld).
var clientOrgPropagationTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "grpc_client_org_propagation_total",
	Help: "Outbound gRPC calls by org-propagation outcome (propagated|inherited|absent|system).",
}, []string{"outcome"})

// serverOrgPropagationTotal counts inbound calls that asserted an org by how it
// was resolved: stamped (verified + stamped active), already_scoped (a prior
// interceptor already resolved the active org), unverified_pass (org asserted
// with no principal — passed UNSTAMPED, never trusted).
var serverOrgPropagationTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "grpc_server_org_propagation_total",
	Help: "Inbound gRPC calls asserting an org by resolution outcome (stamped|already_scoped|unverified_pass).",
}, []string{"outcome"})

// orgPropagationRejectedTotal counts inbound calls rejected because the caller
// asserted an org it is not authorized to act in (a forged/confused-deputy org).
var orgPropagationRejectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "grpc_org_propagation_rejected_total",
	Help: "Inbound gRPC calls rejected for asserting an unauthorized organization.",
}, []string{"caller"})

// skipPropagation reports whether a full-method is infrastructure plumbing
// (health, reflection) that should never carry tenant identity.
func skipPropagation(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.v1.Health/") ||
		strings.HasPrefix(fullMethod, "/grpc.reflection.")
}

// propagateOutgoing fills the tenant/caller identity keys onto the outgoing
// metadata FILL-IF-ABSENT: a key already present (e.g. a hand-set
// x-organization-id, or authorization / x-api-key) is never touched. It uses a
// single AppendToOutgoingContext so it can never drop existing metadata the way
// NewOutgoingContext would.
func propagateOutgoing(ctx context.Context) context.Context {
	md, _ := metadata.FromOutgoingContext(ctx)
	var add []string

	// Organization — the security-relevant key. A BYPASSRLS system path must
	// never ride an org onto the wire even if one is resolvable.
	switch {
	case IsSystemContext(ctx):
		clientOrgPropagationTotal.WithLabelValues("system").Inc()
	case len(md.Get(MDOrganizationID)) > 0:
		clientOrgPropagationTotal.WithLabelValues("inherited").Inc()
	default:
		if org, ok := EffectiveOrg(ctx); ok {
			add = append(add, MDOrganizationID, org.String())
			clientOrgPropagationTotal.WithLabelValues("propagated").Inc()
		} else {
			clientOrgPropagationTotal.WithLabelValues("absent").Inc()
		}
	}

	// User — verified subject wins; else the multi-hop relayed id.
	if len(md.Get(MDUserID)) == 0 {
		if uid, ok := outgoingUserID(ctx); ok {
			add = append(add, MDUserID, uid)
		}
	}

	// Request id — from the logger correlation key.
	if len(md.Get(MDRequestID)) == 0 {
		if rid, ok := ctx.Value(logger.RequestIDKey).(string); ok && rid != "" {
			add = append(add, MDRequestID, rid)
		}
	}

	// Agent / approver — opaque pass-through from typed ctx keys.
	if len(md.Get(MDAgentID)) == 0 {
		if aid, ok := AgentIDFromContext(ctx); ok {
			add = append(add, MDAgentID, aid)
		}
	}
	if len(md.Get(MDApproverID)) == 0 {
		if apid, ok := ApproverIDFromContext(ctx); ok {
			add = append(add, MDApproverID, apid)
		}
	}

	if len(add) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, add...)
	}
	return ctx
}

// outgoingUserID resolves the user id to propagate: the verified JWT subject if
// present, else a user id relayed to us over a prior hop (service principal).
func outgoingUserID(ctx context.Context) (string, bool) {
	if p, ok := FromContext(ctx); ok && p.Claims != nil && p.Claims.Subject != "" {
		return p.Claims.Subject, true
	}
	if uid, ok := PropagatedUserID(ctx); ok {
		return uid.String(), true
	}
	return "", false
}

// UnaryClientPropagation returns a unary client interceptor that fills the
// tenant/caller identity metadata (org, user, request, agent, approver) onto
// every outbound call FILL-IF-ABSENT, so tenant scope survives the hop without
// each caller wiring it by hand. It is behavior-neutral for callers that already
// set the keys. Health/reflection calls are skipped.
func UnaryClientPropagation() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if skipPropagation(method) {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		return invoker(propagateOutgoing(ctx), method, req, reply, cc, opts...)
	}
}

// StreamClientPropagation mirrors [UnaryClientPropagation] for streaming RPCs.
func StreamClientPropagation() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		if skipPropagation(method) {
			return streamer(ctx, desc, cc, method, opts...)
		}
		return streamer(propagateOutgoing(ctx), desc, cc, method, opts...)
	}
}

// inboundPropagation applies the incoming tenant/caller metadata to ctx. It is
// the security core: it stamps an asserted org ONLY after re-verifying the
// caller may act in it (rejecting a forged org), never trusts an x-user-id when
// a verified JWT is present, and never hard-rejects on a missing principal (Auth
// owns rejection — this is only reachable on auth-skipped methods).
func inboundPropagation(ctx context.Context, fullMethod string) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, nil
	}
	orgVals := md.Get(MDOrganizationID)
	userVals := md.Get(MDUserID)
	reqVals := md.Get(MDRequestID)
	agentVals := md.Get(MDAgentID)
	approverVals := md.Get(MDApproverID)

	// Rule 1: no propagation metadata at all → pass through untouched.
	if len(orgVals) == 0 && len(userVals) == 0 && len(reqVals) == 0 && len(agentVals) == 0 && len(approverVals) == 0 {
		return ctx, nil
	}

	p, hasPrincipal := FromContext(ctx)

	// Organization — the forged-org gate.
	if len(orgVals) > 0 {
		if len(orgVals) > 1 {
			return ctx, status.Error(codes.InvalidArgument, "duplicate x-organization-id")
		}
		if raw := orgVals[0]; raw != "" {
			org, err := uuid.Parse(raw)
			if err != nil {
				return ctx, status.Error(codes.InvalidArgument, "invalid x-organization-id")
			}
			switch {
			case activeOrgAlreadySet(ctx):
				// Rule 3: a service's OrgField/metadata interceptor already
				// resolved the active org — do not override it.
				serverOrgPropagationTotal.WithLabelValues("already_scoped").Inc()
			case !hasPrincipal:
				// Rule 4: org asserted with no principal. Never stamp an
				// unverified org; never hard-reject (Auth owns rejection). Pass
				// UNSTAMPED so the callee's tenantdb fails closed on its own.
				logger.FromContext(ctx).Warn("org propagation: asserted org with no principal; passing unstamped",
					"method", fullMethod, "asserted_org", org.String())
				serverOrgPropagationTotal.WithLabelValues("unverified_pass").Inc()
			case p.CanActInOrg(org):
				// Rule 5 (allow): the caller is authorized for the asserted org.
				ctx = WithActiveOrg(ctx, org)
				serverOrgPropagationTotal.WithLabelValues("stamped").Inc()
			default:
				// Rule 5 (deny): forged/confused-deputy org — reject.
				orgPropagationRejectedTotal.WithLabelValues(callerLabel(p)).Inc()
				return ctx, status.Error(codes.PermissionDenied, "caller not authorized for asserted organization")
			}
		}
	}

	// User (rule 6): a verified subject always wins and is never re-attributed;
	// only a SERVICE principal may relay an on-behalf-of user id.
	if len(userVals) > 0 && userVals[0] != "" && p.Claims == nil && (p.ServiceAuthed || p.ServiceSVID != "") {
		if uid, err := uuid.Parse(userVals[0]); err == nil {
			ctx = context.WithValue(ctx, propagatedUserCtxKey{}, uid)
			if existing, ok := ctx.Value(logger.UserIDKey).(string); !ok || existing == "" {
				ctx = context.WithValue(ctx, logger.UserIDKey, uid.String())
			}
		}
	}

	// Request id (rule 7) → logger correlation key if unset.
	if len(reqVals) > 0 && reqVals[0] != "" {
		if existing, ok := ctx.Value(logger.RequestIDKey).(string); !ok || existing == "" {
			ctx = context.WithValue(ctx, logger.RequestIDKey, reqVals[0])
		}
	}

	// Agent / approver — opaque pass-through into typed keys.
	if len(agentVals) > 0 && agentVals[0] != "" {
		ctx = WithAgentID(ctx, agentVals[0])
	}
	if len(approverVals) > 0 && approverVals[0] != "" {
		ctx = WithApproverID(ctx, approverVals[0])
	}

	return ctx, nil
}

// activeOrgAlreadySet reports whether a prior interceptor already resolved the
// request's active org.
func activeOrgAlreadySet(ctx context.Context) bool {
	_, ok := ActiveOrgFromContext(ctx)
	return ok
}

// callerLabel is the metric label for a rejected caller: the service attribution
// when present, else "user" for a claims-only caller, else "unknown".
func callerLabel(p Principal) string {
	if p.Service != "" {
		return p.Service
	}
	if p.Claims != nil {
		return "user"
	}
	return "unknown"
}

// UnaryInboundPropagation returns a unary server interceptor applying the
// incoming tenant/caller metadata to ctx and rejecting a forged org. Register it
// AFTER Auth and any service OrgField interceptor (so a principal + a prior
// active org are both visible).
func UnaryInboundPropagation() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		newCtx, err := inboundPropagation(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// StreamInboundPropagation mirrors [UnaryInboundPropagation] for streaming RPCs,
// applying propagation at stream start.
func StreamInboundPropagation() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		newCtx, err := inboundPropagation(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &propagationServerStream{ServerStream: ss, ctx: newCtx})
	}
}

// propagationServerStream wraps a grpc.ServerStream with a modified context. It
// is a local copy of the interceptor package's unexported wrappedServerStream
// (copied per the tenant→interceptor cycle constraint — the interceptor type is
// not exported).
type propagationServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *propagationServerStream) Context() context.Context { return w.ctx }
