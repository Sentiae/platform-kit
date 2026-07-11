package tenant

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// newOrgFieldMessage builds a dynamic proto message with a single string
// "organization_id" field set to the given value. It uses dynamicpb from the
// protobuf module already imported by the interceptor — no new dependency.
func newOrgFieldMessage(t *testing.T, org string) proto.Message {
	t.Helper()
	strKind := descriptorpb.FieldDescriptorProto_TYPE_STRING
	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	fdProto := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("orgfield_test.proto"),
		Package: proto.String("tenant.orgfieldtest"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("OrgReq"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:   proto.String("organization_id"),
				Number: proto.Int32(1),
				Type:   &strKind,
				Label:  &label,
			}},
		}},
	}
	fd, err := protodesc.NewFile(fdProto, nil)
	if err != nil {
		t.Fatalf("build file descriptor: %v", err)
	}
	md := fd.Messages().Get(0)
	msg := dynamicpb.NewMessage(md)
	if org != "" {
		msg.Set(md.Fields().ByName("organization_id"), protoreflect.ValueOfString(org))
	}
	return msg
}

// captureHandler returns a unary handler that records the ctx it was called
// with, so a test can assert what the interceptor stamped.
func captureHandler(got *context.Context) grpc.UnaryHandler {
	return func(ctx context.Context, _ any) (any, error) {
		*got = ctx
		return "ok", nil
	}
}

func TestUnaryOrgField(t *testing.T) {
	interceptor := UnaryOrgField()
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Do"}

	t.Run("valid org is stamped onto ctx", func(t *testing.T) {
		var seen context.Context
		req := newOrgFieldMessage(t, orgA.String())
		resp, err := interceptor(context.Background(), req, info, captureHandler(&seen))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "ok" {
			t.Fatalf("handler resp = %v, want ok", resp)
		}
		got, ok := ActiveOrgFromContext(seen)
		if !ok {
			t.Fatal("expected active org on ctx")
		}
		if got != orgA {
			t.Fatalf("active org = %v, want %v", got, orgA)
		}
	})

	t.Run("empty org leaves ctx unchanged", func(t *testing.T) {
		var seen context.Context
		req := newOrgFieldMessage(t, "")
		if _, err := interceptor(context.Background(), req, info, captureHandler(&seen)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := ActiveOrgFromContext(seen); ok {
			t.Fatal("expected no active org on ctx for empty organization_id")
		}
	})

	t.Run("malformed org rejected as InvalidArgument", func(t *testing.T) {
		var seen context.Context
		req := newOrgFieldMessage(t, "not-a-uuid")
		_, err := interceptor(context.Background(), req, info, captureHandler(&seen))
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("err code = %v, want InvalidArgument", status.Code(err))
		}
		if seen != nil {
			t.Fatal("handler must not run on malformed organization_id")
		}
	})

	t.Run("non-proto request passes through", func(t *testing.T) {
		var seen context.Context
		if _, err := interceptor(context.Background(), "plain-string", info, captureHandler(&seen)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := ActiveOrgFromContext(seen); ok {
			t.Fatal("expected no active org for a non-proto request")
		}
	})

	t.Run("message without organization_id passes through", func(t *testing.T) {
		var seen context.Context
		// status.Status is a proto message with no organization_id field.
		req := status.New(codes.OK, "").Proto()
		if _, err := interceptor(context.Background(), req, info, captureHandler(&seen)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := ActiveOrgFromContext(seen); ok {
			t.Fatal("expected no active org when field is absent")
		}
	})
}

// TestStreamOrgField confirms the stream counterpart is a pass-through.
func TestStreamOrgField(t *testing.T) {
	called := false
	err := StreamOrgField()(nil, nil, &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/Stream"},
		func(_ any, _ grpc.ServerStream) error {
			called = true
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("stream handler must be invoked")
	}
}
