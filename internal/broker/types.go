package broker

import "context"

// Message represents a single chat message from OpenAI-compatible API.
type Message struct {
	Role       string `json:"role"`
	Content    any    `json:"content"`
	ToolCalls  []any  `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ContentText extracts plain text from message content (handles both string and array forms).
func (m Message) ContentText() string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []any:
		var out string
		for _, item := range v {
			if mp, ok := item.(map[string]any); ok {
				if text, ok := mp["text"].(string); ok {
					out += text + "\n"
				}
			}
		}
		return out
	}
	return ""
}

// ChatRequest is the parsed structure of an OpenAI chat completion request.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Tools    []any     `json:"tools,omitempty"`
}

// EscalationSignal indicates whether escalation is needed.
type EscalationSignal struct {
	ShouldEscalate bool
	Reason         string
	FailureCount   int
	Pattern        string // "empty_tool_calls", "tool_errors", "model_refusal", "mixed"
}

// Permission is the policy engine's decision on whether escalation is allowed.
type Permission struct {
	Allowed  bool
	Reason   string
	Findings []Finding
}

// Finding is one policy evaluation result.
type Finding struct {
	Policy     string   `json:"policy"`
	Verdict    string   `json:"verdict"` // "clean", "suspicious", "violation"
	Confidence float64  `json:"confidence"`
	Details    []string `json:"details"`
}

// ShaperResult is the output of context shaping — a minimal clean prompt for the external model.
type ShaperResult struct {
	Body          []byte
	Summary       string
	TokenEstimate int
}

// ValidationResult describes whether an external response is acceptable.
type ValidationResult struct {
	Valid  bool
	Reason string
}

// --- Pluggable interfaces ---

// FailureDetector analyzes conversation history for escalation signals.
type FailureDetector interface {
	Analyze(req *ChatRequest) EscalationSignal
}

// PolicyEngine evaluates content against security policies via LLM.
type PolicyEngine interface {
	Evaluate(ctx context.Context, req *ChatRequest) (*Permission, error)
}

// ContextShaper minimizes and cleans context for the external model via LLM.
type ContextShaper interface {
	Shape(ctx context.Context, req *ChatRequest, perm *Permission, targetModel string) (*ShaperResult, error)
}

// ResponseValidator checks that the external model's response is usable.
type ResponseValidator interface {
	Validate(ctx context.Context, response []byte) (*ValidationResult, error)
}

// Escalator sends a shaped request to an external model (or a stub).
type Escalator interface {
	Forward(ctx context.Context, body []byte) ([]byte, error)
}
