package tenant

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryServiceAuthz enforces per-method least privilege (Tier-2) for a set of
// protected gRPC full-methods. A SERVICE caller (peer SVID present) invoking a
// protected method must have AllowsMethod on its grant; otherwise the call is
// PermissionDenied. Calls with no peer SVID (user-JWT calls) pass through —
// RPC-level protection targets service-to-service callers. An empty `protected`
// map is a no-op, so this is safe to install unconditionally and opt in per
// callee by populating the set. Lives in package tenant (not interceptor) so it
// can consult ServiceGrants without the interceptor→tenant→interceptor cycle.
func UnaryServiceAuthz(protected map[string]struct{}, grants ServiceGrants) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, ok := protected[info.FullMethod]; ok {
			if p, _ := FromContext(ctx); p.ServiceSVID != "" && !grants.AllowsMethod(p.ServiceSVID, info.FullMethod) {
				return nil, status.Errorf(codes.PermissionDenied, "service %s not authorized for %s", p.ServiceSVID, info.FullMethod)
			}
		}
		return handler(ctx, req)
	}
}

// StreamServiceAuthz mirrors [UnaryServiceAuthz] for streaming RPCs.
func StreamServiceAuthz(protected map[string]struct{}, grants ServiceGrants) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if _, ok := protected[info.FullMethod]; ok {
			if p, _ := FromContext(ss.Context()); p.ServiceSVID != "" && !grants.AllowsMethod(p.ServiceSVID, info.FullMethod) {
				return status.Errorf(codes.PermissionDenied, "service %s not authorized for %s", p.ServiceSVID, info.FullMethod)
			}
		}
		return handler(srv, ss)
	}
}
