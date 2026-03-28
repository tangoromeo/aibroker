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

// LLMEndpoint configures a connection to an OpenAI-compatible LLM.
type LLMEndpoint struct {
	URL     string            `yaml:"url"`
	Model   string            `yaml:"model"`
	APIKey  string            `yaml:"api_key"`
	Timeout time.Duration     `yaml:"timeout"`
	Headers map[string]string `yaml:"headers"`
}

// LLMClient calls an OpenAI-compatible chat completion endpoint.
type LLMClient struct {
	endpoint string
	model    string
	apiKey   string
	headers  map[string]string
	client   *http.Client
	logger   *slog.Logger
}

func NewLLMClient(cfg LLMEndpoint, logger *slog.Logger) *LLMClient {
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	endpoint := strings.TrimRight(cfg.URL, "/")
	if !strings.HasSuffix(endpoint, "/v1/chat/completions") {
		endpoint += "/v1/chat/completions"
	}
	return &LLMClient{
		endpoint: endpoint,
		model:    cfg.Model,
		apiKey:   cfg.APIKey,
		headers:  cfg.Headers,
		client:   &http.Client{Timeout: cfg.Timeout},
		logger:   logger,
	}
}

// Complete sends a system+user message pair and returns the assistant's text response.
func (c *LLMClient) Complete(ctx context.Context, system, user string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0,
		"stream":      false,
	})

	respBody, err := c.doPost(ctx, body)
	if err != nil {
		return "", err
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse LLM response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty LLM response")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// Forward sends an arbitrary chat completion request body and returns the raw response.
func (c *LLMClient) Forward(ctx context.Context, body []byte) ([]byte, error) {
	return c.doPost(ctx, body)
}

func (c *LLMClient) doPost(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM returned %d: %s", resp.StatusCode, truncStr(string(respBody), 300))
	}
	return respBody, nil
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
