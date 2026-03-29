package httpproxy

import (
	"net/http"
	"strings"

	"aibroker/internal/config"
)

// ClientKind distinguishes the HTTP client so we can apply different auth routing.
// Kilo and other OpenAI-compatible tools share ClientOpenAICompat.
type ClientKind int

const (
	ClientOpenAICompat ClientKind = iota
	ClientContinue
)

// DetectClientKind classifies the incoming request. When cfg is nil, all clients
// are treated as OpenAI-compatible.
func DetectClientKind(r *http.Request, cfg *config.ClientRoutingConfig) ClientKind {
	if cfg == nil {
		return ClientOpenAICompat
	}
	cc := cfg.Continue
	for _, sub := range cc.UserAgentSubstrings {
		if sub == "" {
			continue
		}
		if strings.Contains(strings.ToLower(r.UserAgent()), strings.ToLower(sub)) {
			return ClientContinue
		}
	}
	if cc.HeaderPresent != "" && r.Header.Get(cc.HeaderPresent) != "" {
		return ClientContinue
	}
	return ClientOpenAICompat
}
