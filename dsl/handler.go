// Package dsl ships the reusable HTTP handler every Sentiae service
// mounts at `POST /dsl/execute` to accept step dispatches from
// foundry-service's FlowDSLWorker. §19 follow-up — centralising the
// endpoint here means each service only registers its action
// handlers and doesn't redo request parsing / error mapping.
//
// Contract the foundry HTTPDispatcher already emits:
//
//	{
//	  "flow_id": "uuid",
//	  "step_name": "string",
//	  "action": "string",
//	  "config": {...},
//	  "input":  {...}
//	}
//
// Response shape:
//
//	{
//	  "output":    {...},
//	  "error":     "string" (optional),
//	  "completed": bool
//	}
package dsl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// Request mirrors foundry's DSLDispatchRequest so callers can import
// this package without a dependency on foundry-service.
type Request struct {
	FlowID   uuid.UUID      `json:"flow_id"`
	StepName string         `json:"step_name"`
	Action   string         `json:"action,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
}

// Response mirrors foundry's DSLDispatchResult.
type Response struct {
	Output    map[string]any `json:"output,omitempty"`
	Error     string         `json:"error,omitempty"`
	Completed bool           `json:"completed"`
}

// Action is the server-side handler a service registers for one DSL
// action name. The action sees the parsed request and returns the
// output map (or an error, which becomes a 5xx + error response body).
type Action func(ctx context.Context, req Request) (map[string]any, error)

// Handler is the reusable HTTP surface. Construct once per service,
// register actions during DI, mount at `POST /dsl/execute`.
type Handler struct {
	mu      sync.RWMutex
	actions map[string]Action
}

// NewHandler builds an empty handler.
func NewHandler() *Handler {
	return &Handler{actions: make(map[string]Action)}
}

// Register records the action handler for `name`. Calling Register
// twice for the same name replaces the earlier handler — services
// typically declare their actions in one init call, so collisions
// are a bug to surface loudly if we ever see them in logs.
func (h *Handler) Register(name string, action Action) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actions[name] = action
}

// ServeHTTP implements http.Handler. Parses the request, looks up the
// action, calls it, and writes the response. Unknown actions return
// 400; action errors return 500 with the error in the response body.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Action == "" {
		writeError(w, http.StatusBadRequest, "action is required")
		return
	}

	h.mu.RLock()
	action, ok := h.actions[req.Action]
	h.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown action: "+req.Action)
		return
	}

	output, err := action(r.Context(), req)
	if err != nil {
		// Distinguish structured dispatch errors from plain failures:
		// an ActionError writes the message into the response body
		// with a 2xx so the flow worker records the error alongside
		// the step output. Plain errors become 500.
		var ae *ActionError
		if errors.As(err, &ae) {
			writeJSON(w, http.StatusOK, Response{Error: ae.Error(), Output: output})
			return
		}
		writeJSON(w, http.StatusInternalServerError, Response{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, Response{Output: output, Completed: true})
}

// ActionError is a typed wrapper actions return when they want the
// foundry-side flow to record a step failure without treating it as a
// transport-level error. Usage:
//
//	return nil, &dsl.ActionError{Msg: "spec already exists"}
type ActionError struct {
	Msg string
}

// Error implements the error interface.
func (e *ActionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

func writeJSON(w http.ResponseWriter, status int, body Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{Error: msg})
}
