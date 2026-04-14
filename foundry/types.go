package foundry

import "time"

// DispatchRequest is the unified input for all foundry operations.
type DispatchRequest struct {
	Operation      string         `json:"operation"`
	OrganizationID string         `json:"organization_id"`
	UserID         string         `json:"user_id,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	Priority       string         `json:"priority,omitempty"` // critical, default, low
	CallbackURL    string         `json:"callback_url,omitempty"`
	BudgetCapUSD   float64        `json:"budget_cap_usd,omitempty"`
}

// DispatchResult is the unified output for all foundry operations.
type DispatchResult struct {
	ID         string         `json:"id"`
	Operation  string         `json:"operation"`
	Status     string         `json:"status"`
	Data       map[string]any `json:"data,omitempty"`
	Error      string         `json:"error,omitempty"`
	CostUSD    float64        `json:"cost_usd,omitempty"`
	TokensUsed int            `json:"tokens_used,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	ModelUsed  string         `json:"model_used,omitempty"`
	Provider   string         `json:"provider,omitempty"`
	CacheHit   bool           `json:"cache_hit,omitempty"`
}

// AsyncJob represents the status of an asynchronous dispatch job.
type AsyncJob struct {
	ID          string          `json:"id"`
	Operation   string          `json:"operation"`
	Status      string          `json:"status"` // pending, running, completed, failed
	Result      *DispatchResult `json:"result,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

// StreamChunk represents a single chunk in a streaming response.
type StreamChunk struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// Capabilities represents the foundry service's available operations and resources.
type Capabilities struct {
	Operations     []Operation `json:"operations"`
	OperationCount int         `json:"operation_count"`
	Categories     []string    `json:"categories"`
	Providers      any         `json:"providers"`
	Budget         any         `json:"budget,omitempty"`
}

// Operation represents a single foundry operation capability.
type Operation struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    string            `json:"category"`
	Parameters  map[string]string `json:"parameters"`
	Examples    []string          `json:"examples"`
	Guardrails  Guardrails        `json:"guardrails"`
}

// Guardrails defines safety constraints for an operation.
type Guardrails struct {
	RequiresConfirmation bool    `json:"requires_confirmation"`
	RequiresSession      bool    `json:"requires_session"`
	MaxCostUSD           float64 `json:"max_cost_usd"`
	ReadOnly             bool    `json:"read_only"`
	Destructive          bool    `json:"destructive"`
}
