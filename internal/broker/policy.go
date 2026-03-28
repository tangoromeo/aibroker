package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// PolicyConfig defines a single screening policy.
type PolicyConfig struct {
	Name        string `yaml:"name"`
	Severity    string `yaml:"severity"` // critical, high, medium, low
	Action      string `yaml:"action"`   // block, warn
	Description string `yaml:"description"`
	Rules       string `yaml:"rules"`
}

const screeningPromptTemplate = `Return one JSON object only. No markdown.

Policy: %s

Rules (real violations only):
%s

NEVER count as violation (verdict MUST be "clean" if these are the only "issues"):
- Already-redacted text: <REDACTED>, <OBJECT_ID>, <PROJECT_ID>, <NUMERIC_ID>, <TEST_CARD_DOC_EXAMPLE>, <COPYRIGHT_HOLDER>
- Env syntax: ${VAR}
- example.com / example.org / user@example.com
- Names or years in Copyright / LICENSE / SPDX / "Author:" boilerplate lines
- Test card number 1234 5678 9012 3456 (documentation example, not a real card)
- A bare long number without @ is NOT an email address

Do NOT invent findings. If unsure, verdict "clean".

Output exactly:
{"verdict":"clean","confidence":0.95,"findings":[]}
or
{"verdict":"violation","confidence":0.9,"findings":["short reason"]}
`

// LLMPolicyEngine evaluates content against policies using a local LLM.
type LLMPolicyEngine struct {
	client   *LLMClient
	policies []PolicyConfig
	logger   *slog.Logger
}

func NewLLMPolicyEngine(client *LLMClient, policies []PolicyConfig, logger *slog.Logger) *LLMPolicyEngine {
	return &LLMPolicyEngine{client: client, policies: policies, logger: logger}
}

func (e *LLMPolicyEngine) Evaluate(ctx context.Context, req *ChatRequest) (*Permission, error) {
	content := extractContent(req)
	if content == "" {
		return &Permission{Allowed: true, Reason: "no content to screen"}, nil
	}
	content = normalizeForScreening(content)
	if len(content) > 8000 {
		content = content[:8000] + "\n...(truncated)"
	}

	type result struct {
		idx     int
		finding Finding
		err     error
	}

	ch := make(chan result, len(e.policies))
	for i, pol := range e.policies {
		go func(idx int, p PolicyConfig) {
			e.logger.Info("policy eval started", "policy", p.Name)
			prompt := fmt.Sprintf(screeningPromptTemplate, p.Description, p.Rules)
			resp, err := e.client.Complete(ctx, prompt, content)
			if err != nil {
				ch <- result{idx: idx, err: fmt.Errorf("policy %s: %w", p.Name, err)}
				return
			}
			v, err := parseVerdict(resp)
			if err != nil {
				ch <- result{idx: idx, err: fmt.Errorf("policy %s: %w", p.Name, err)}
				return
			}
			if v.Verdict == "suspicious" && v.Confidence == 0 {
				e.logger.Warn("policy LLM returned non-JSON",
					"policy", p.Name,
					"raw_response", truncStr(resp, 500),
				)
			}
			ch <- result{idx: idx, finding: Finding{
				Policy:     p.Name,
				Verdict:    v.Verdict,
				Confidence: v.Confidence,
				Details:    v.Findings,
			}}
		}(i, pol)
	}

	var findings []Finding
	blocked := false

	for range e.policies {
		r := <-ch
		if r.err != nil {
			e.logger.Error("policy eval error", "err", r.err)
			continue
		}
		findings = append(findings, r.finding)
		pol := e.policies[r.idx]

		e.logger.Info("policy eval done",
			"policy", r.finding.Policy,
			"verdict", r.finding.Verdict,
			"confidence", r.finding.Confidence,
			"findings", r.finding.Details,
		)

		if r.finding.Verdict == "violation" && pol.Action == "block" {
			blocked = true
		}
	}

	if blocked {
		return &Permission{
			Allowed:  false,
			Reason:   "blocked by security policy",
			Findings: findings,
		}, nil
	}
	return &Permission{
		Allowed:  true,
		Reason:   "passed screening",
		Findings: findings,
	}, nil
}

// --- helpers ---

// extractContent pulls ALL message text from the chat request for screening.
func extractContent(req *ChatRequest) string {
	var sb strings.Builder
	for _, msg := range req.Messages {
		text := msg.ContentText()
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "[%s]: %s\n---\n", msg.Role, text)
	}
	return sb.String()
}

// Verdict is the expected JSON structure from the screening LLM.
type Verdict struct {
	Verdict  string   `json:"verdict"`
	Confidence float64  `json:"confidence"`
	Findings []string `json:"findings"`
}

func parseVerdict(raw string) (*Verdict, error) {
	content := strings.TrimSpace(raw)

	// Strip markdown code blocks
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

	// Find JSON object boundaries
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		content = content[start : end+1]

		var v Verdict
		if err := json.Unmarshal([]byte(content), &v); err == nil {
			return &v, nil
		}
	}

	// LLM refused or returned non-JSON — treat as unable to evaluate.
	// Fail-safe: flag as suspicious so screen doesn't silently pass.
	return &Verdict{
		Verdict:    "suspicious",
		Confidence: 0,
		Findings:   []string{"screening LLM did not return structured verdict"},
	}, nil
}
