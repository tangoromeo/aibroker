package broker

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// PatternDetector analyzes recent conversation history for failure patterns.
type PatternDetector struct {
	minFailures int
	cooldown    time.Duration
	lastTrigger time.Time
	mu          sync.Mutex
	logger      *slog.Logger
}

func NewPatternDetector(minFailures int, logger *slog.Logger) *PatternDetector {
	if minFailures <= 0 {
		minFailures = 3
	}
	return &PatternDetector{
		minFailures: minFailures,
		cooldown:    60 * time.Second,
		logger:      logger,
	}
}

func (d *PatternDetector) Analyze(req *ChatRequest) EscalationSignal {
	d.mu.Lock()
	if !d.lastTrigger.IsZero() && time.Since(d.lastTrigger) < d.cooldown {
		d.mu.Unlock()
		return EscalationSignal{}
	}
	d.mu.Unlock()

	msgs := req.Messages
	window := 20
	if len(msgs) < window {
		window = len(msgs)
	}

	var emptyToolCalls, toolErrors, refusals int

	for i := len(msgs) - window; i < len(msgs); i++ {
		msg := msgs[i]
		switch msg.Role {
		case "assistant":
			emptyToolCalls += countEmptyToolCalls(msg)
			if isRefusal(msg.ContentText()) {
				refusals++
			}
		case "tool":
			if isToolError(msg.ContentText()) {
				toolErrors++
			}
		}
	}

	total := emptyToolCalls + toolErrors + refusals
	if total < d.minFailures {
		return EscalationSignal{}
	}

	pattern := "mixed"
	switch {
	case emptyToolCalls >= d.minFailures:
		pattern = "empty_tool_calls"
	case toolErrors >= d.minFailures:
		pattern = "tool_errors"
	case refusals >= d.minFailures:
		pattern = "model_refusal"
	}

	d.mu.Lock()
	d.lastTrigger = time.Now()
	d.mu.Unlock()

	return EscalationSignal{
		ShouldEscalate: true,
		Reason:         fmt.Sprintf("%d failures detected (empty_tc=%d, tool_err=%d, refusals=%d)", total, emptyToolCalls, toolErrors, refusals),
		FailureCount:   total,
		Pattern:        pattern,
	}
}

func countEmptyToolCalls(msg Message) int {
	if msg.ToolCalls == nil {
		return 0
	}
	count := 0
	for _, tc := range msg.ToolCalls {
		tcMap, ok := tc.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tcMap["function"].(map[string]any)
		if !ok {
			continue
		}
		args, _ := fn["arguments"].(string)
		if args == "" || args == "{}" {
			count++
		}
	}
	return count
}

func isRefusal(text string) bool {
	lower := strings.ToLower(text)
	refusalPhrases := []string{
		"i cannot", "i can't", "i'm unable", "i am unable",
		"i don't know how", "i do not know how",
		"beyond my capabilities", "outside my capabilities",
		"not able to", "unable to help",
	}
	for _, p := range refusalPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func isToolError(text string) bool {
	lower := strings.ToLower(text)
	errorPhrases := []string{
		"error:", "failed:", "missing value for required parameter",
		"tool execution failed", "retrying",
	}
	for _, p := range errorPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
