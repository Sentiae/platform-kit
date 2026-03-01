package errors

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestError_Error(t *testing.T) {
	e := &Error{
		Code:       "not_found",
		Message:    "user not found",
		HTTPStatus: http.StatusNotFound,
		GRPCCode:   codes.NotFound,
	}
	if got := e.Error(); got != "user not found" {
		t.Errorf("Error() = %q, want %q", got, "user not found")
	}
}

func TestRegister_And_ToHTTP(t *testing.T) {
	t.Cleanup(func() { Reset() })

	errNotFound := errors.New("not found")
	errBadInput := errors.New("bad input")

	Register(errNotFound, http.StatusNotFound, codes.NotFound)
	Register(errBadInput, http.StatusBadRequest, codes.InvalidArgument)

	t.Run("direct sentinel", func(t *testing.T) {
		code, resp := ToHTTP(errNotFound)
		if code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", code, http.StatusNotFound)
		}
		if resp.Status != http.StatusNotFound {
			t.Errorf("resp.Status = %d, want %d", resp.Status, http.StatusNotFound)
		}
		if resp.Title != "Not Found" {
			t.Errorf("resp.Title = %q, want %q", resp.Title, "Not Found")
		}
		if resp.Detail != "not found" {
			t.Errorf("resp.Detail = %q, want %q", resp.Detail, "not found")
		}
		if resp.Type != "about:blank" {
			t.Errorf("resp.Type = %q, want %q", resp.Type, "about:blank")
		}
	})

	t.Run("wrapped sentinel", func(t *testing.T) {
		wrapped := fmt.Errorf("loading user: %w", errNotFound)
		code, resp := ToHTTP(wrapped)
		if code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", code, http.StatusNotFound)
		}
		if resp.Detail != "loading user: not found" {
			t.Errorf("resp.Detail = %q, want %q", resp.Detail, "loading user: not found")
		}
	})

	t.Run("unregistered error", func(t *testing.T) {
		unknown := errors.New("something unexpected")
		code, resp := ToHTTP(unknown)
		if code != http.StatusInternalServerError {
			t.Errorf("status = %d, want %d", code, http.StatusInternalServerError)
		}
		if resp.Status != http.StatusInternalServerError {
			t.Errorf("resp.Status = %d, want %d", resp.Status, http.StatusInternalServerError)
		}
	})
}

func TestRegister_And_ToGRPC(t *testing.T) {
	t.Cleanup(func() { Reset() })

	errUnauthorized := errors.New("unauthorized")
	Register(errUnauthorized, http.StatusUnauthorized, codes.Unauthenticated)

	t.Run("direct sentinel", func(t *testing.T) {
		grpcErr := ToGRPC(errUnauthorized)
		st, ok := status.FromError(grpcErr)
		if !ok {
			t.Fatal("expected gRPC status error")
		}
		if st.Code() != codes.Unauthenticated {
			t.Errorf("code = %v, want %v", st.Code(), codes.Unauthenticated)
		}
		if st.Message() != "unauthorized" {
			t.Errorf("message = %q, want %q", st.Message(), "unauthorized")
		}
	})

	t.Run("wrapped sentinel", func(t *testing.T) {
		wrapped := fmt.Errorf("auth check: %w", errUnauthorized)
		grpcErr := ToGRPC(wrapped)
		st, ok := status.FromError(grpcErr)
		if !ok {
			t.Fatal("expected gRPC status error")
		}
		if st.Code() != codes.Unauthenticated {
			t.Errorf("code = %v, want %v", st.Code(), codes.Unauthenticated)
		}
	})

	t.Run("unregistered error", func(t *testing.T) {
		grpcErr := ToGRPC(errors.New("boom"))
		st, ok := status.FromError(grpcErr)
		if !ok {
			t.Fatal("expected gRPC status error")
		}
		if st.Code() != codes.Internal {
			t.Errorf("code = %v, want %v", st.Code(), codes.Internal)
		}
	})
}

func TestToHTTP_PlatformError(t *testing.T) {
	t.Cleanup(func() { Reset() })

	pe := &Error{
		Code:       "conflict",
		Message:    "already exists",
		HTTPStatus: http.StatusConflict,
		GRPCCode:   codes.AlreadyExists,
	}

	code, resp := ToHTTP(pe)
	if code != http.StatusConflict {
		t.Errorf("status = %d, want %d", code, http.StatusConflict)
	}
	if resp.Detail != "already exists" {
		t.Errorf("resp.Detail = %q, want %q", resp.Detail, "already exists")
	}
}

func TestToGRPC_PlatformError(t *testing.T) {
	t.Cleanup(func() { Reset() })

	pe := &Error{
		Code:       "conflict",
		Message:    "already exists",
		HTTPStatus: http.StatusConflict,
		GRPCCode:   codes.AlreadyExists,
	}

	grpcErr := ToGRPC(pe)
	st, ok := status.FromError(grpcErr)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if st.Code() != codes.AlreadyExists {
		t.Errorf("code = %v, want %v", st.Code(), codes.AlreadyExists)
	}
	if st.Message() != "already exists" {
		t.Errorf("message = %q, want %q", st.Message(), "already exists")
	}
}

func TestRegister_Overwrite(t *testing.T) {
	t.Cleanup(func() { Reset() })

	errTest := errors.New("test error")
	Register(errTest, http.StatusBadRequest, codes.InvalidArgument)
	Register(errTest, http.StatusNotFound, codes.NotFound)

	code, _ := ToHTTP(errTest)
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (overwritten)", code, http.StatusNotFound)
	}
}

func TestReset(t *testing.T) {
	errTest := errors.New("test")
	Register(errTest, http.StatusBadRequest, codes.InvalidArgument)
	Reset()

	code, _ := ToHTTP(errTest)
	if code != http.StatusInternalServerError {
		t.Errorf("after Reset, status = %d, want %d", code, http.StatusInternalServerError)
	}
}

func TestToHTTP_WrappedPlatformError(t *testing.T) {
	t.Cleanup(func() { Reset() })

	pe := &Error{
		Code:       "forbidden",
		Message:    "access denied",
		HTTPStatus: http.StatusForbidden,
		GRPCCode:   codes.PermissionDenied,
	}
	wrapped := fmt.Errorf("handler: %w", pe)

	code, resp := ToHTTP(wrapped)
	if code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", code, http.StatusForbidden)
	}
	if resp.Detail != "access denied" {
		t.Errorf("resp.Detail = %q, want %q", resp.Detail, "access denied")
	}
}
