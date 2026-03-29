package httpproxy

import (
	"fmt"
	"net/http"
	"strings"

	"aibroker/internal/config"
)

func parseBearerToken(authz string) (string, bool) {
	const prefix = "Bearer "
	if len(authz) < len(prefix) || !strings.EqualFold(authz[:len(prefix)], prefix) {
		return "", false
	}
	t := strings.TrimSpace(authz[len(prefix):])
	if t == "" {
		return "", false
	}
	return t, true
}

// mergeSplitAPIKey builds the full upstream API key from bearer token + extra header.
func mergeSplitAPIKey(r *http.Request, join string, extraHeader string) (string, error) {
	if extraHeader == "" {
		return "", fmt.Errorf("auth_from_client: extra header name not configured")
	}
	bearer, ok := parseBearerToken(r.Header.Get("Authorization"))
	if !ok {
		return "", fmt.Errorf("missing or invalid Authorization Bearer for split auth")
	}
	suffix := strings.TrimSpace(r.Header.Get(extraHeader))
	if suffix == "" {
		return "", fmt.Errorf("missing %s for split auth", extraHeader)
	}
	switch strings.ToLower(join) {
	case "", "bearer_first":
		return bearer + suffix, nil
	case "header_first":
		return suffix + bearer, nil
	default:
		return "", fmt.Errorf("auth_from_client.join must be bearer_first or header_first, got %q", join)
	}
}

func resolveSplitExtraHeader(kind ClientKind, cfg *config.AuthFromClientConfig, routing *config.ClientRoutingConfig) string {
	if cfg == nil || !cfg.Enabled {
		return ""
	}
	if kind == ClientContinue && routing != nil && routing.Continue.ExtraHeader != "" {
		return routing.Continue.ExtraHeader
	}
	return cfg.ExtraHeader
}

// colonBearerSplitContinue parses Authorization Bearer "openai-key:some-id" (first ':' only).
// Upstream Authorization is Bearer openai-key; some-id is sent in idHeader.
func colonBearerSplitContinue(r *http.Request, idHeader string) (authorization string, extra map[string]string, err error) {
	if idHeader == "" {
		return "", nil, fmt.Errorf("colon_bearer_split: id_header not configured")
	}
	token, ok := parseBearerToken(r.Header.Get("Authorization"))
	if !ok {
		return "", nil, fmt.Errorf("missing or invalid Authorization Bearer for colon_bearer_split")
	}
	keyPart, idPart, ok := strings.Cut(token, ":")
	if !ok {
		return "", nil, fmt.Errorf("Authorization Bearer must contain ':' (key:id) for colon_bearer_split")
	}
	keyPart = strings.TrimSpace(keyPart)
	idPart = strings.TrimSpace(idPart)
	if keyPart == "" || idPart == "" {
		return "", nil, fmt.Errorf("Authorization Bearer key:id must have non-empty key and id for colon_bearer_split")
	}
	return "Bearer " + keyPart, map[string]string{idHeader: idPart}, nil
}
