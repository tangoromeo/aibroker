package httpproxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

type ContextTrimConfig struct {
	MaxTokens        int // approximate token budget for messages
	SystemMaxTokens  int // max tokens for system prompt specifically
	PreserveLastN    int // always keep last N user/assistant messages
}

func ContextTrim(cfg ContextTrimConfig, logger *slog.Logger) Middleware {
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8192
	}
	if cfg.SystemMaxTokens == 0 {
		cfg.SystemMaxTokens = cfg.MaxTokens / 2
	}
	if cfg.PreserveLastN == 0 {
		cfg.PreserveLastN = 4
	}

	return func(next Handler) Handler {
		return func(ctx context.Context, req *ProxyRequest) (*http.Response, error) {
			var body map[string]json.RawMessage
			if err := json.Unmarshal(req.BodyRaw, &body); err != nil {
				return next(ctx, req)
			}

			msgsRaw, ok := body["messages"]
			if !ok {
				return next(ctx, req)
			}

			var msgs []chatMessage
			if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
				return next(ctx, req)
			}

			originalTokens := estimateTokens(msgs)
			if originalTokens <= cfg.MaxTokens {
				return next(ctx, req)
			}

			trimmed := trimMessages(msgs, cfg)
			trimmedTokens := estimateTokens(trimmed)

			logger.Info("context_trim",
				"original_tokens", originalTokens,
				"trimmed_tokens", trimmedTokens,
				"original_msgs", len(msgs),
				"trimmed_msgs", len(trimmed),
			)

			newMsgs, _ := json.Marshal(trimmed)
			body["messages"] = newMsgs
			req.BodyRaw, _ = json.Marshal(body)

			return next(ctx, req)
		}
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
	rest    map[string]json.RawMessage
}

func (m *chatMessage) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["role"]; ok {
		_ = json.Unmarshal(v, &m.Role)
	}
	if v, ok := raw["content"]; ok {
		_ = json.Unmarshal(v, &m.Content)
	}
	m.rest = raw
	return nil
}

func (m chatMessage) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.rest)
}

func trimMessages(msgs []chatMessage, cfg ContextTrimConfig) []chatMessage {
	if len(msgs) == 0 {
		return msgs
	}

	var result []chatMessage

	// 1. Handle system message(s) — truncate if oversized
	startIdx := 0
	for startIdx < len(msgs) && msgs[startIdx].Role == "system" {
		msg := msgs[startIdx]
		tokens := estimateMessageTokens(msg)
		if tokens > cfg.SystemMaxTokens {
			msg = truncateMessage(msg, cfg.SystemMaxTokens)
		}
		result = append(result, msg)
		startIdx++
	}

	remaining := msgs[startIdx:]
	if len(remaining) == 0 {
		return result
	}

	// 2. Always keep last N messages
	preserveCount := cfg.PreserveLastN
	if preserveCount > len(remaining) {
		preserveCount = len(remaining)
	}
	tail := remaining[len(remaining)-preserveCount:]
	middle := remaining[:len(remaining)-preserveCount]

	// 3. Budget: total - system - tail
	budget := cfg.MaxTokens - estimateTokens(result) - estimateTokens(tail)

	// 4. Fill from middle, oldest first, until budget exhausted
	for _, msg := range middle {
		cost := estimateMessageTokens(msg)
		if budget-cost < 0 {
			break
		}
		budget -= cost
		result = append(result, msg)
	}

	result = append(result, tail...)
	return result
}

func truncateMessage(msg chatMessage, maxTokens int) chatMessage {
	s := contentString(msg)
	maxChars := maxTokens * 4 // ~4 chars per token
	if len(s) <= maxChars {
		return msg
	}

	truncated := s[:maxChars] + "\n...[truncated by proxy]"
	newRaw, _ := json.Marshal(truncated)
	msg.rest["content"] = newRaw
	msg.Content = truncated
	return msg
}

func estimateTokens(msgs []chatMessage) int {
	total := 0
	for _, m := range msgs {
		total += estimateMessageTokens(m)
	}
	return total
}

func estimateMessageTokens(m chatMessage) int {
	s := contentString(m)
	// Rough approximation: 1 token ≈ 4 chars for English, ~2-3 for code
	return len(s)/4 + 4 // +4 for role/message overhead
}

func contentString(m chatMessage) string {
	switch v := m.Content.(type) {
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
