package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

const shapingSystemPromptTemplate = `You are a context minimization and anonymization engine for a code escalation system.

replace every occurrence of the following with generic placeholders:
%s
Everything that match any of this rules should be replaced with <REDACTED>

Formulate a clear, concise question.

CRITICAL: The output MUST NOT contain any real corporate names, domains, credentials, or identifiers. If in doubt, replace it.

Respond with ONLY this JSON (no markdown, no explanation):
{
  "question": "Clear one-line description of the problem",
  "code_context": "Relevant code (anonymized, only what's needed)",
  "language": "programming language",
  "constraints": "Important constraints or requirements (empty string if none)"
}`

// LLMContextShaper uses a local LLM to minimize conversation context for escalation.
type LLMContextShaper struct {
	client       *LLMClient
	systemPrompt string
	logger       *slog.Logger
}

func NewLLMContextShaper(client *LLMClient, policies []PolicyConfig, logger *slog.Logger) *LLMContextShaper {
	var rules strings.Builder
	for _, p := range policies {
		fmt.Fprintf(&rules, "[%s] %s:\n%s\n", p.Name, p.Description, p.Rules)
	}
	prompt := fmt.Sprintf(shapingSystemPromptTemplate, rules.String())

	return &LLMContextShaper{client: client, systemPrompt: prompt, logger: logger}
}

func (s *LLMContextShaper) Shape(ctx context.Context, req *ChatRequest, perm *Permission, targetModel string) (*ShaperResult, error) {
	conversation := buildConversationSummary(req)
	// Keep generous limit — screening LLM needs to see the actual code.
	if len(conversation) > 32000 {
		conversation = conversation[len(conversation)-32000:]
	}

	s.logger.Debug("shape: calling LLM", "conversation_bytes", len(conversation))

	resp, err := s.client.Complete(ctx, s.systemPrompt, conversation)
	if err != nil {
		return nil, fmt.Errorf("context shaping LLM call: %w", err)
	}

	shaped, err := parseShaped(resp)
	if err != nil {
		return nil, err
	}

	sanitizeShaped(shaped, s.logger)

	// Build a clean chat completion request for the external model
	userContent := shaped.Question
	if shaped.CodeContext != "" {
		userContent += "\n\n```" + shaped.Language + "\n" + shaped.CodeContext + "\n```"
	}
	if shaped.Constraints != "" {
		userContent += "\n\nConstraints: " + shaped.Constraints
	}

	body, _ := json.Marshal(map[string]any{
		"model": targetModel,
		"messages": []map[string]string{
			{"role": "system", "content": "You are an expert programmer. Provide a clear, concise, correct solution. Return ONLY the solution code and a brief explanation."},
			{"role": "user", "content": userContent},
		},
		"temperature": 0,
		"stream":      false,
	})

	s.logger.Info("context shaped",
		"question", truncStr(shaped.Question, 120),
		"code_bytes", len(shaped.CodeContext),
		"language", shaped.Language,
		"output_bytes", len(body),
	)

	return &ShaperResult{
		Body:          body,
		Summary:       shaped.Question,
		TokenEstimate: len(body) / 4,
	}, nil
}

func buildConversationSummary(req *ChatRequest) string {
	var sb strings.Builder
	for _, msg := range req.Messages {
		text := msg.ContentText()
		if text == "" {
			continue
		}
		// System prompt from Kilo is huge but mostly instructions.
		// Keep only a tail that may contain file/environment details.
		if msg.Role == "system" && len(text) > 4000 {
			text = "...(system prompt truncated)\n" + text[len(text)-4000:]
		}
		fmt.Fprintf(&sb, "[%s]: %s\n\n", msg.Role, text)
	}
	return sb.String()
}

type shapedContext struct {
	Question    string `json:"question"`
	CodeContext string `json:"code_context"`
	Language    string `json:"language"`
	Constraints string `json:"constraints"`
}

func parseShaped(raw string) (*shapedContext, error) {
	content := strings.TrimSpace(raw)

	if strings.Contains(content, "```") {
		lines := strings.Split(content, "\n")
		var jsonLines []string
		inBlock := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				jsonLines = append(jsonLines, line)
			}
		}
		if len(jsonLines) > 0 {
			content = strings.Join(jsonLines, "\n")
		}
	}

	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		content = content[start : end+1]
	}

	var s shapedContext
	if err := json.Unmarshal([]byte(content), &s); err != nil {
		return nil, fmt.Errorf("parse shaped context: %w (raw: %.300s)", err, raw)
	}
	return &s, nil
}
