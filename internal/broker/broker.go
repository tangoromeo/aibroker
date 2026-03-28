package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"aibroker/internal/httpproxy"
)

type Config struct {
	ScreeningLLM    LLMEndpoint    `yaml:"screening"`
	ExternalLLM     LLMEndpoint    `yaml:"escalation"`
	Policies        []PolicyConfig `yaml:"policies"`
	MinFailures     int            `yaml:"min_failures"`
	ForceEscalation bool
	EscalationMode  string
	StubDir         string
}

// Registry holds all broker middleware, keyed by name for the pipeline.
type Registry struct {
	mw map[string]httpproxy.Middleware
}

// Build creates a Registry of broker middleware from config.
// Each middleware can be used independently in the pipeline YAML.
func Build(cfg Config, logger *slog.Logger) *Registry {
	log := logger.With("component", "broker")
	screenClient := NewLLMClient(cfg.ScreeningLLM, log)

	var esc Escalator
	if cfg.EscalationMode == "stub" {
		dir := cfg.StubDir
		if dir == "" {
			dir = "escalation_dumps"
		}
		esc = NewStubEscalator(dir, log)
	} else {
		esc = NewLLMClient(cfg.ExternalLLM, log)
	}

	var detector FailureDetector
	if cfg.ForceEscalation {
		detector = AlwaysDetector{}
		log.Warn("force_escalation enabled — every request will trigger escalation pipeline")
	} else {
		detector = NewPatternDetector(cfg.MinFailures, log)
	}
	policy := NewLLMPolicyEngine(screenClient, cfg.Policies, log)
	shaper := NewLLMContextShaper(screenClient, cfg.Policies, log)
	validator := NewBasicValidator()

	return &Registry{
		mw: map[string]httpproxy.Middleware{
			"escalation_detect":  DetectMiddleware(detector, log),
			"escalation_screen":  ScreenMiddleware(policy, log),
			"escalation_shape":   ShapeMiddleware(shaper, cfg.ExternalLLM.Model, log),
			"escalation_forward": ForwardMiddleware(esc, validator, cfg.ExternalLLM.Model, log),
		},
	}
}

// Get returns a middleware by name, or nil if not found.
func (r *Registry) Get(name string) httpproxy.Middleware {
	if r == nil {
		return nil
	}
	return r.mw[name]
}

// Names returns all registered middleware names.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	var names []string
	for k := range r.mw {
		names = append(names, k)
	}
	return names
}

func syntheticResponse(body []byte, originalModel string, stream bool) *http.Response {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err == nil {
		resp["model"] = originalModel
		resp["_escalated"] = true
		body, _ = json.Marshal(resp)
	}

	if !stream {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(body)),
		}
	}

	// Wrap as SSE for streaming clients.
	sseBody := toSSE(body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewReader(sseBody)),
	}
}

// toSSE converts a non-streaming chat completion response into SSE format.
func toSSE(body []byte) []byte {
	var resp struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Choices) == 0 {
		var buf bytes.Buffer
		buf.WriteString("data: ")
		buf.Write(body)
		buf.WriteString("\n\ndata: [DONE]\n\n")
		return buf.Bytes()
	}

	chunk, _ := json.Marshal(map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion.chunk",
		"created": resp.Created,
		"model":   resp.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]string{
					"role":    resp.Choices[0].Message.Role,
					"content": resp.Choices[0].Message.Content,
				},
				"finish_reason": "stop",
			},
		},
	})

	var buf bytes.Buffer
	buf.WriteString("data: ")
	buf.Write(chunk)
	buf.WriteString("\n\ndata: [DONE]\n\n")
	return buf.Bytes()
}

func fmtFindings(findings []Finding) string {
	var parts []string
	for _, f := range findings {
		parts = append(parts, fmt.Sprintf("%s:%s(%.0f%%)", f.Policy, f.Verdict, f.Confidence*100))
	}
	return strings.Join(parts, ", ")
}
