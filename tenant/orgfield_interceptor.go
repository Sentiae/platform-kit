package tenant

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// orgFieldName is the conventional request field naming the organization a
// unary RPC acts in. Requests carrying it opt into ctx org-stamping.
const orgFieldName protoreflect.Name = "organization_id"

// UnaryOrgField returns a unary server interceptor that annotates the request
// context with the active org named by the request's string "organization_id"
// field, so that tenantdb can stamp the tenant GUC for RLS-scoped reads.
//
// It is pure plumbing: it does NOT authorize. Authorization remains in
// tenantdb.resolveOrg's CanActInOrg re-verify against the resolved Principal;
// this interceptor only moves the caller-supplied org onto ctx so that layer
// can re-check it. Registered before the tenant-authorizing layer in the chain.
//
// Behavior per unary call:
//   - req is not a proto.Message, or has no string "organization_id" field, or
//     the field is empty -> ctx passed through unchanged.
//   - the field is a non-empty string that fails uuid parsing -> the call is
//     rejected with codes.InvalidArgument.
//   - the field is a valid uuid -> handler runs with WithActiveOrg(ctx, org).
func UnaryOrgField() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		raw, ok := orgFieldValue(req)
		if !ok || raw == "" {
			return handler(ctx, req)
		}
		org, err := uuid.Parse(raw)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid organization_id")
		}
		return handler(WithActiveOrg(ctx, org), req)
	}
}

// StreamOrgField returns a stream server interceptor counterpart to
// UnaryOrgField so callers can register both symmetrically. A stream has no
// single request to reflect on, so this is a pass-through: stream org-scoping is
// handler-resolved (the handler stamps the org itself from the messages it
// reads). It exists only for registration symmetry.
func StreamOrgField() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, ss)
	}
}

// orgFieldValue reflects on req and returns its string "organization_id" field
// value. ok is false when req is not a proto.Message or has no such string
// field.
func orgFieldValue(req any) (value string, ok bool) {
	m, isMsg := req.(proto.Message)
	if !isMsg {
		return "", false
	}
	fd := m.ProtoReflect().Descriptor().Fields().ByName(orgFieldName)
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return "", false
	}
	return m.ProtoReflect().Get(fd).String(), true
}
