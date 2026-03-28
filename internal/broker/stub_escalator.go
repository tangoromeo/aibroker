package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
)

// StubEscalator writes shaped prompts to files instead of calling an external model.
// Returns a synthetic response so the full pipeline can be demonstrated end-to-end.
type StubEscalator struct {
	dir    string
	seq    atomic.Int64
	logger *slog.Logger
}

func NewStubEscalator(dir string, logger *slog.Logger) *StubEscalator {
	_ = os.MkdirAll(dir, 0o755)
	return &StubEscalator{dir: dir, logger: logger}
}

// Forward saves the shaped request to a file and returns an error
// so the broker falls back to the local model.
// The captured file shows what WOULD be sent in live mode.
func (s *StubEscalator) Forward(_ context.Context, body []byte) ([]byte, error) {
	n := s.seq.Add(1)
	path := filepath.Join(s.dir, fmt.Sprintf("escalation_%d.json", n))

	if err := os.WriteFile(path, body, 0o644); err != nil {
		s.logger.Error("stub: write failed", "path", path, "err", err)
	}

	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)

	question := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			question = truncStr(m.Content, 200)
			break
		}
	}

	s.logger.Warn("stub: escalation captured (not sent) → falling back to local model",
		"path", path,
		"model", req.Model,
		"question", question,
		"bytes", len(body),
	)

	return nil, fmt.Errorf("stub mode: escalation saved to %s, falling back to local", path)
}
