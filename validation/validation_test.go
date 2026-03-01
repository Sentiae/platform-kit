package validation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type testInput struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=8,max=128"`
	Name     string `json:"name" validate:"required,min=2"`
	Role     string `json:"role" validate:"omitempty,oneof=admin user viewer"`
}

func TestValidateRequest_Success(t *testing.T) {
	body := `{"email":"test@example.com","password":"Password123","name":"John"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))

	var input testInput
	err := ValidateRequest(r, &input)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if input.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", input.Email)
	}
	if input.Password != "Password123" {
		t.Errorf("expected password Password123, got %s", input.Password)
	}
}

func TestValidateRequest_InvalidJSON(t *testing.T) {
	body := `{invalid`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))

	var input testInput
	err := ValidateRequest(r, &input)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	// Should not be a ValidationError
	var ve *ValidationError
	if asValidationError(err, &ve) {
		t.Error("expected non-ValidationError for invalid JSON")
	}
}

func TestValidateRequest_MissingRequiredFields(t *testing.T) {
	body := `{}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))

	var input testInput
	err := ValidateRequest(r, &input)
	if err == nil {
		t.Fatal("expected validation error")
	}

	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}

	// Should have 3 errors: email, password, name
	if len(ve.Fields) != 3 {
		t.Errorf("expected 3 field errors, got %d: %v", len(ve.Fields), ve.Fields)
	}

	fieldMap := make(map[string]string)
	for _, f := range ve.Fields {
		fieldMap[f.Field] = f.Message
	}

	if msg, ok := fieldMap["email"]; !ok || msg != "is required" {
		t.Errorf("expected email 'is required', got %q", msg)
	}
	if msg, ok := fieldMap["password"]; !ok || msg != "is required" {
		t.Errorf("expected password 'is required', got %q", msg)
	}
	if msg, ok := fieldMap["name"]; !ok || msg != "is required" {
		t.Errorf("expected name 'is required', got %q", msg)
	}
}

func TestValidateRequest_InvalidEmail(t *testing.T) {
	body := `{"email":"notanemail","password":"Password123","name":"John"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))

	var input testInput
	err := ValidateRequest(r, &input)
	if err == nil {
		t.Fatal("expected validation error")
	}

	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if len(ve.Fields) != 1 {
		t.Fatalf("expected 1 field error, got %d", len(ve.Fields))
	}
	if ve.Fields[0].Field != "email" {
		t.Errorf("expected field 'email', got %q", ve.Fields[0].Field)
	}
	if ve.Fields[0].Message != "must be a valid email address" {
		t.Errorf("expected email error message, got %q", ve.Fields[0].Message)
	}
}

func TestValidateRequest_PasswordTooShort(t *testing.T) {
	body := `{"email":"test@example.com","password":"short","name":"John"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))

	var input testInput
	err := ValidateRequest(r, &input)
	if err == nil {
		t.Fatal("expected validation error")
	}

	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if len(ve.Fields) != 1 {
		t.Fatalf("expected 1 field error, got %d", len(ve.Fields))
	}
	if ve.Fields[0].Field != "password" {
		t.Errorf("expected field 'password', got %q", ve.Fields[0].Field)
	}
	if ve.Fields[0].Message != "must be at least 8 characters" {
		t.Errorf("expected min length message, got %q", ve.Fields[0].Message)
	}
}

func TestValidateRequest_InvalidOneof(t *testing.T) {
	body := `{"email":"test@example.com","password":"Password123","name":"John","role":"superadmin"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))

	var input testInput
	err := ValidateRequest(r, &input)
	if err == nil {
		t.Fatal("expected validation error")
	}

	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if len(ve.Fields) != 1 {
		t.Fatalf("expected 1 field error, got %d", len(ve.Fields))
	}
	if ve.Fields[0].Field != "role" {
		t.Errorf("expected field 'role', got %q", ve.Fields[0].Field)
	}
}

func TestValidateStruct_Success(t *testing.T) {
	input := testInput{
		Email:    "test@example.com",
		Password: "Password123",
		Name:     "John",
	}
	if err := ValidateStruct(&input); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidationError_Error(t *testing.T) {
	ve := &ValidationError{
		Fields: []FieldError{
			{Field: "email", Message: "is required"},
			{Field: "name", Message: "is required"},
		},
	}
	got := ve.Error()
	if got != "validation failed: email: is required; name: is required" {
		t.Errorf("unexpected error string: %s", got)
	}
}

func TestWriteProblemDetails_ValidationError(t *testing.T) {
	w := httptest.NewRecorder()
	ve := &ValidationError{
		Fields: []FieldError{
			{Field: "email", Message: "is required"},
		},
	}

	WriteProblemDetails(w, ve)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("expected content-type application/problem+json, got %q", ct)
	}

	var pd ProblemDetails
	if err := json.NewDecoder(w.Body).Decode(&pd); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if pd.Type != "about:blank" {
		t.Errorf("expected type about:blank, got %q", pd.Type)
	}
	if pd.Title != "Bad Request" {
		t.Errorf("expected title Bad Request, got %q", pd.Title)
	}
	if pd.Status != 400 {
		t.Errorf("expected status 400, got %d", pd.Status)
	}
	if len(pd.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(pd.Errors))
	}
	if pd.Errors[0].Field != "email" {
		t.Errorf("expected field email, got %q", pd.Errors[0].Field)
	}
}

func TestWriteProblemDetails_NonValidationError(t *testing.T) {
	w := httptest.NewRecorder()
	var dummy int
	WriteProblemDetails(w, json.Unmarshal([]byte("bad"), &dummy))

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var pd ProblemDetails
	if err := json.NewDecoder(w.Body).Decode(&pd); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(pd.Errors) != 0 {
		t.Errorf("expected no field errors, got %d", len(pd.Errors))
	}
}

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"FullName", "full_name"},
		{"email", "email"},
		{"IPAddress", "i_p_address"},
		{"Name", "name"},
	}
	for _, tt := range tests {
		got := toSnakeCase(tt.input)
		if got != tt.want {
			t.Errorf("toSnakeCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
