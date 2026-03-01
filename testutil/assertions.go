package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	platformerrors "github.com/sentiae/platform-kit/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AssertGRPCStatus asserts that err is a gRPC status error with the expected code.
// It calls t.Fatal if the assertion fails.
//
// Example:
//
//	_, err := client.GetUser(ctx, req)
//	testutil.AssertGRPCStatus(t, err, codes.NotFound)
func AssertGRPCStatus(t *testing.T, err error, expectedCode codes.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("AssertGRPCStatus: expected gRPC error with code %s, got nil", expectedCode)
		return
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("AssertGRPCStatus: expected gRPC status error, got: %T: %v", err, err)
		return
	}

	if st.Code() != expectedCode {
		t.Fatalf("AssertGRPCStatus: expected code %s, got %s (message: %q)", expectedCode, st.Code(), st.Message())
	}
}

// AssertGRPCOK asserts that err is nil (i.e. the gRPC call succeeded).
//
// Example:
//
//	resp, err := client.GetUser(ctx, req)
//	testutil.AssertGRPCOK(t, err)
func AssertGRPCOK(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		st, ok := status.FromError(err)
		if ok {
			t.Fatalf("AssertGRPCOK: expected OK, got %s: %s", st.Code(), st.Message())
		} else {
			t.Fatalf("AssertGRPCOK: expected nil error, got: %v", err)
		}
	}
}

// AssertProblemDetails reads an HTTP response body and asserts it matches
// RFC 7807 ProblemDetails with the given HTTP status and error type string.
// The errorType is matched against the "type" field; pass empty string to
// skip that check.
//
// Example:
//
//	resp := httptest.NewRecorder()
//	handler.ServeHTTP(resp, req)
//	testutil.AssertProblemDetails(t, resp.Result(), http.StatusNotFound, "")
func AssertProblemDetails(t *testing.T, resp *http.Response, expectedStatus int, errorType string) {
	t.Helper()

	if resp.StatusCode != expectedStatus {
		t.Fatalf("AssertProblemDetails: expected status %d, got %d", expectedStatus, resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/problem+json" && ct != "application/json" {
		t.Fatalf("AssertProblemDetails: expected Content-Type application/problem+json or application/json, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("AssertProblemDetails: read body: %v", err)
	}

	var pd platformerrors.ErrorResponse
	if err := json.Unmarshal(body, &pd); err != nil {
		t.Fatalf("AssertProblemDetails: unmarshal ProblemDetails: %v\nbody: %s", err, body)
	}

	if pd.Status != expectedStatus {
		t.Fatalf("AssertProblemDetails: expected ProblemDetails.Status %d, got %d", expectedStatus, pd.Status)
	}

	if errorType != "" && pd.Type != errorType {
		t.Fatalf("AssertProblemDetails: expected ProblemDetails.Type %q, got %q", errorType, pd.Type)
	}

	if pd.Title == "" {
		t.Fatal("AssertProblemDetails: ProblemDetails.Title is empty")
	}
}
