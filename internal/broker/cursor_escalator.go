package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CursorAgentsConfig drives escalation via Cursor Cloud Agents API (not Chat Completions).
// Spec: https://cursor.com/docs-static/cloud-agents-openapi.yaml
// Overview: https://cursor.com/docs/cloud-agent/api/endpoints
//
// Flow: POST /v0/agents → poll GET /v0/agents/{id} until FINISHED|ERROR →
// GET /v0/agents/{id}/conversation → map last assistant_message to OpenAI chat response JSON.
type CursorAgentsConfig struct {
	BaseURL      string        `yaml:"base_url"`       // default https://api.cursor.com
	APIKey       string        `yaml:"api_key"`
	Repository   string        `yaml:"repository"`     // required GitHub HTTPS URL
	Ref          string        `yaml:"ref"`            // branch/tag, default main
	Model        string        `yaml:"model"`          // "default" or id from GET /v0/models
	PollInterval time.Duration `yaml:"poll_interval"`  // default 5s
	MaxWait      time.Duration `yaml:"max_wait"`       // default 15m
	HTTPClient   *http.Client  `yaml:"-"`
}

// CursorAgentsEscalator implements Escalator for Cursor Cloud Agents.
type CursorAgentsEscalator struct {
	cfg    CursorAgentsConfig
	logger *slog.Logger
	client *http.Client
}

func NewCursorAgentsEscalator(cfg CursorAgentsConfig, logger *slog.Logger) *CursorAgentsEscalator {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.cursor.com"
	}
	if cfg.Ref == "" {
		cfg.Ref = "main"
	}
	if cfg.Model == "" {
		cfg.Model = "default"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 15 * time.Minute
	}
	c := cfg.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: 120 * time.Second}
	}
	return &CursorAgentsEscalator{cfg: cfg, logger: logger, client: c}
}

// Forward launches a cloud agent with prompt text from shaped OpenAI body, waits for completion,
// returns a synthetic OpenAI-compatible chat completion JSON (non-streaming).
func (c *CursorAgentsEscalator) Forward(ctx context.Context, body []byte) ([]byte, error) {
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return nil, fmt.Errorf("cursor_agents: api_key is required")
	}
	if strings.TrimSpace(c.cfg.Repository) == "" {
		return nil, fmt.Errorf("cursor_agents: repository (GitHub URL) is required")
	}

	promptText, err := extractUserPromptFromShapedBody(body)
	if err != nil {
		return nil, fmt.Errorf("cursor_agents: %w", err)
	}

	base := strings.TrimRight(c.cfg.BaseURL, "/")
	agentID, err := c.createAgent(ctx, base, promptText)
	if err != nil {
		return nil, err
	}

	if err := c.waitAgent(ctx, base, agentID); err != nil {
		return nil, err
	}

	text, err := c.lastAssistantText(ctx, base, agentID)
	if err != nil {
		return nil, err
	}

	return buildOpenAIChatCompletionJSON(c.cfg.Model, text), nil
}

func extractUserPromptFromShapedBody(body []byte) (string, error) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", fmt.Errorf("parse shaped body: %w", err)
	}
	var parts []string
	for _, m := range req.Messages {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			parts = append(parts, strings.TrimSpace(m.Content))
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no user message in shaped body")
	}
	return strings.Join(parts, "\n\n"), nil
}

func (c *CursorAgentsEscalator) createAgent(ctx context.Context, base, prompt string) (string, error) {
	payload := map[string]any{
		"prompt": map[string]string{"text": prompt},
		"model":  c.cfg.Model,
		"source": map[string]string{
			"repository": c.cfg.Repository,
			"ref":        c.cfg.Ref,
		},
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v0/agents", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.cfg.APIKey, "")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("create agent: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create agent: HTTP %d: %s", resp.StatusCode, truncStr(string(raw), 400))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("create agent response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("create agent: empty id")
	}
	c.logger.Info("cursor_agents: agent created", "id", out.ID)
	return out.ID, nil
}

func (c *CursorAgentsEscalator) waitAgent(ctx context.Context, base, id string) error {
	deadline := time.Now().Add(c.cfg.MaxWait)

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("cursor_agents: timeout waiting for agent %s", id)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v0/agents/"+id, nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth(c.cfg.APIKey, "")

		resp, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("poll agent: %w", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("poll agent: HTTP %d: %s", resp.StatusCode, truncStr(string(raw), 300))
		}
		var st struct {
			Status  string `json:"status"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(raw, &st)
		switch st.Status {
		case "FINISHED":
			c.logger.Info("cursor_agents: agent finished", "id", id, "summary", truncStr(st.Summary, 120))
			return nil
		case "ERROR", "EXPIRED":
			return fmt.Errorf("cursor_agents: agent %s status=%s", id, st.Status)
		default:
			c.logger.Debug("cursor_agents: polling", "id", id, "status", st.Status)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.PollInterval):
		}
	}
}

func (c *CursorAgentsEscalator) lastAssistantText(ctx context.Context, base, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v0/agents/"+id+"/conversation", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.cfg.APIKey, "")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("conversation: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("conversation: HTTP %d: %s", resp.StatusCode, truncStr(string(raw), 400))
	}
	var conv struct {
		Messages []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &conv); err != nil {
		return "", fmt.Errorf("parse conversation: %w", err)
	}
	var last string
	for _, m := range conv.Messages {
		if m.Type == "assistant_message" && strings.TrimSpace(m.Text) != "" {
			last = m.Text
		}
	}
	if last == "" {
		return "", fmt.Errorf("cursor_agents: no assistant_message in conversation")
	}
	return last, nil
}

func buildOpenAIChatCompletionJSON(model, assistantText string) []byte {
	id := fmt.Sprintf("chatcmpl-cursor-%d", time.Now().UnixNano())
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": assistantText,
				},
				"finish_reason": "stop",
			},
		},
	}
	b, _ := json.Marshal(out)
	return b
}
