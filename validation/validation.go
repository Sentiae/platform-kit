// Package validation provides request validation using go-playground/validator.
//
// Use ValidateRequest to decode a JSON request body and validate struct tags
// in a single call. Validation errors are returned as a ValidationError that
// can be rendered as RFC 7807 ProblemDetails.
package validation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"
)

var validate = validator.New(validator.WithRequiredStructEnabled())

// FieldError represents a single field validation error.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError is returned when request validation fails.
// It carries field-level details for the client.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	msgs := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		msgs[i] = fmt.Sprintf("%s: %s", f.Field, f.Message)
	}
	return "validation failed: " + strings.Join(msgs, "; ")
}

// ProblemDetails represents an RFC 7807 error response with field-level validation errors.
type ProblemDetails struct {
	Type   string       `json:"type"`
	Title  string       `json:"title"`
	Status int          `json:"status"`
	Detail string       `json:"detail,omitempty"`
	Errors []FieldError `json:"errors,omitempty"`
}

// ValidateRequest decodes the JSON body of r into target and validates struct tags.
// Returns nil on success, *ValidationError on validation failure, or a plain error
// if the JSON body cannot be decoded.
func ValidateRequest(r *http.Request, target any) error {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return ValidateStruct(target)
}

// ValidateStruct validates a struct using go-playground/validator tags.
// Returns *ValidationError on failure, nil on success.
func ValidateStruct(s any) error {
	if err := validate.Struct(s); err != nil {
		var validationErrs validator.ValidationErrors
		if ok := isValidationErrors(err, &validationErrs); ok {
			fields := make([]FieldError, 0, len(validationErrs))
			for _, ve := range validationErrs {
				fields = append(fields, FieldError{
					Field:   jsonFieldName(ve),
					Message: fieldMessage(ve),
				})
			}
			return &ValidationError{Fields: fields}
		}
		return err
	}
	return nil
}

// WriteProblemDetails writes an RFC 7807 JSON response for a ValidationError.
// If err is not a *ValidationError, it writes a generic 400 response.
func WriteProblemDetails(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/problem+json")

	var ve *ValidationError
	if asValidationError(err, &ve) {
		pd := ProblemDetails{
			Type:   "about:blank",
			Title:  "Bad Request",
			Status: http.StatusBadRequest,
			Detail: "Validation failed",
			Errors: ve.Fields,
		}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(pd)
		return
	}

	pd := ProblemDetails{
		Type:   "about:blank",
		Title:  "Bad Request",
		Status: http.StatusBadRequest,
		Detail: err.Error(),
	}
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(pd)
}

// isValidationErrors unwraps err as validator.ValidationErrors.
func isValidationErrors(err error, target *validator.ValidationErrors) bool {
	if ve, ok := err.(validator.ValidationErrors); ok {
		*target = ve
		return true
	}
	return false
}

// asValidationError unwraps err as *ValidationError.
func asValidationError(err error, target **ValidationError) bool {
	if ve, ok := err.(*ValidationError); ok {
		*target = ve
		return true
	}
	return false
}

// jsonFieldName extracts the JSON field name from the struct tag, falling back
// to the Go field name in snake_case style.
func jsonFieldName(fe validator.FieldError) string {
	// Use the Namespace minus the struct name for nested fields
	ns := fe.Namespace()
	if idx := strings.Index(ns, "."); idx != -1 {
		ns = ns[idx+1:]
	}
	return toSnakeCase(ns)
}

// fieldMessage generates a human-readable message for a validation error.
func fieldMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "min":
		return fmt.Sprintf("must be at least %s characters", fe.Param())
	case "max":
		return fmt.Sprintf("must be at most %s characters", fe.Param())
	case "oneof":
		return fmt.Sprintf("must be one of: %s", fe.Param())
	case "uuid":
		return "must be a valid UUID"
	case "url":
		return "must be a valid URL"
	case "gte":
		return fmt.Sprintf("must be greater than or equal to %s", fe.Param())
	case "lte":
		return fmt.Sprintf("must be less than or equal to %s", fe.Param())
	default:
		return fmt.Sprintf("failed on '%s' validation", fe.Tag())
	}
}

// toSnakeCase converts a PascalCase or camelCase string to snake_case.
func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				result.WriteByte('_')
			}
			result.WriteRune(r + 32) // lowercase
		} else if r == '.' {
			result.WriteByte('.')
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}
