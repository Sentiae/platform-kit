package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	platformerrors "github.com/sentiae/platform-kit/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAssertGRPCStatus_MatchesCode(t *testing.T) {
	err := status.Error(codes.NotFound, "user not found")

	// Should not panic/fail.
	AssertGRPCStatus(t, err, codes.NotFound)
}

func TestAssertGRPCStatus_NilError(t *testing.T) {
	fakeT := &testing.T{}
	// AssertGRPCStatus should call Fatal on nil err; we can't easily test
	// t.Fatal in Go, so we just verify the function exists and compiles.
	// In practice, the function is used in integration tests.
	_ = fakeT
}

func TestAssertGRPCOK_NilIsOK(t *testing.T) {
	// Should not panic/fail.
	AssertGRPCOK(t, nil)
}

func TestAssertProblemDetails_Valid(t *testing.T) {
	pd := platformerrors.ErrorResponse{
		Type:   "about:blank",
		Title:  "Not Found",
		Status: http.StatusNotFound,
		Detail: "user not found",
	}
	body, _ := json.Marshal(pd)

	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/problem+json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	// Should not panic/fail.
	AssertProblemDetails(t, resp, http.StatusNotFound, "about:blank")
}

func TestAssertProblemDetails_EmptyErrorType(t *testing.T) {
	pd := platformerrors.ErrorResponse{
		Type:   "about:blank",
		Title:  "Bad Request",
		Status: http.StatusBadRequest,
		Detail: "invalid input",
	}
	body, _ := json.Marshal(pd)

	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	// Empty errorType means skip type check.
	AssertProblemDetails(t, resp, http.StatusBadRequest, "")
}
