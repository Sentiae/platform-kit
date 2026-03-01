// Package errors provides a standardized error type system with HTTP and gRPC mappings.
//
// Define domain errors as sentinel values, then register them with HTTP/gRPC codes:
//
//	var ErrNotFound = errors.New("not found")
//	platformerrors.Register(ErrNotFound, http.StatusNotFound, codes.NotFound)
//
// Convert errors at the boundary:
//
//	status, resp := platformerrors.ToHTTP(err)   // RFC 7807 ProblemDetails
//	grpcErr := platformerrors.ToGRPC(err)        // status.Error
package errors

import (
	"errors"
	"net/http"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error is the platform's standardized error type. It carries both an HTTP
// status code and a gRPC status code alongside a machine-readable Code
// and human-readable Message.
type Error struct {
	// Code is a machine-readable error identifier (e.g. "not_found", "unauthorized").
	Code string
	// Message is a human-readable description.
	Message string
	// HTTPStatus is the HTTP status code for this error.
	HTTPStatus int
	// GRPCCode is the gRPC status code for this error.
	GRPCCode codes.Code
}

func (e *Error) Error() string {
	return e.Message
}

// ErrorResponse represents an RFC 7807 Problem Details JSON response.
type ErrorResponse struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// registration holds a sentinel error and its HTTP/gRPC codes.
type registration struct {
	sentinel   error
	httpStatus int
	grpcCode   codes.Code
}

var (
	registrations []registration
	registryMu    sync.RWMutex
)

// Register associates a domain error (sentinel) with HTTP and gRPC status codes.
// Uses errors.Is for matching, so wrapped errors are correctly resolved.
// Calling Register multiple times for the same sentinel overwrites the previous mapping.
func Register(domainErr error, httpStatus int, grpcCode codes.Code) {
	registryMu.Lock()
	defer registryMu.Unlock()

	// Overwrite if already registered.
	for i, r := range registrations {
		if errors.Is(r.sentinel, domainErr) {
			registrations[i] = registration{
				sentinel:   domainErr,
				httpStatus: httpStatus,
				grpcCode:   grpcCode,
			}
			return
		}
	}

	registrations = append(registrations, registration{
		sentinel:   domainErr,
		httpStatus: httpStatus,
		grpcCode:   grpcCode,
	})
}

// ToHTTP converts an error to an HTTP status code and an RFC 7807 ErrorResponse.
// If the error (or a wrapped error) is registered, its mapped status is used.
// Unregistered errors default to 500 Internal Server Error.
func ToHTTP(err error) (int, ErrorResponse) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	// Check if the error itself is a platform *Error.
	var platformErr *Error
	if errors.As(err, &platformErr) {
		return platformErr.HTTPStatus, ErrorResponse{
			Type:   "about:blank",
			Title:  http.StatusText(platformErr.HTTPStatus),
			Status: platformErr.HTTPStatus,
			Detail: platformErr.Message,
		}
	}

	// Walk registrations to find a matching sentinel via errors.Is.
	for _, r := range registrations {
		if errors.Is(err, r.sentinel) {
			return r.httpStatus, ErrorResponse{
				Type:   "about:blank",
				Title:  http.StatusText(r.httpStatus),
				Status: r.httpStatus,
				Detail: err.Error(),
			}
		}
	}

	return http.StatusInternalServerError, ErrorResponse{
		Type:   "about:blank",
		Title:  http.StatusText(http.StatusInternalServerError),
		Status: http.StatusInternalServerError,
		Detail: err.Error(),
	}
}

// ToGRPC converts an error to a gRPC status.Error.
// If the error (or a wrapped error) is registered, its mapped gRPC code is used.
// Unregistered errors default to codes.Internal.
func ToGRPC(err error) error {
	registryMu.RLock()
	defer registryMu.RUnlock()

	// Check if the error itself is a platform *Error.
	var platformErr *Error
	if errors.As(err, &platformErr) {
		return status.Error(platformErr.GRPCCode, platformErr.Message)
	}

	// Walk registrations to find a matching sentinel via errors.Is.
	for _, r := range registrations {
		if errors.Is(err, r.sentinel) {
			return status.Error(r.grpcCode, err.Error())
		}
	}

	return status.Error(codes.Internal, err.Error())
}

// Reset clears all registered error mappings. Intended for use in tests only.
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registrations = nil
}
