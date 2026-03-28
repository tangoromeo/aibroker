package httpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// ToolAdapter simplifies OpenAI tool definitions to improve compatibility
// with models that struggle with strict function calling schemas.
//
// Request: removes strict, additionalProperties, fixes union types.
// Response (non-streaming): adds back missing nullable required fields.
func ToolAdapter(logger *slog.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req *ProxyRequest) (*http.Response, error) {
			if !strings.HasSuffix(req.HTTP.URL.Path, "/chat/completions") {
				return next(ctx, req)
			}

			var body map[string]json.RawMessage
			if err := json.Unmarshal(req.BodyRaw, &body); err != nil {
				return next(ctx, req)
			}

			rawTools, ok := body["tools"]
			if !ok {
				return next(ctx, req)
			}

			var tools []any
			if err := json.Unmarshal(rawTools, &tools); err != nil || len(tools) == 0 {
				return next(ctx, req)
			}

			// Store originals before modification.
			origSchemas := buildOrigSchemas(tools)

			// Simplify each tool definition.
			simplified := 0
			for i, tool := range tools {
				t, ok := tool.(map[string]any)
				if !ok {
					continue
				}
				fn, ok := t["function"].(map[string]any)
				if !ok {
					continue
				}
				delete(fn, "strict")
				if params, ok := fn["parameters"].(map[string]any); ok {
					simplifySchema(params)
					simplified++
				}
				tools[i] = t
			}

			if simplified == 0 {
				return next(ctx, req)
			}

			newToolsJSON, _ := json.Marshal(tools)
			body["tools"] = newToolsJSON
			newBody, _ := json.Marshal(body)
			req.BodyRaw = newBody

			logger.Debug("tool_adapter: simplified tools", "count", simplified)

			resp, err := next(ctx, req)
			if err != nil || resp == nil {
				return resp, err
			}

			// For streaming responses, pass through — rely on schema simplification.
			if req.Stream || isSSE(resp) {
				return resp, nil
			}

			// Non-streaming: normalize tool_call arguments.
			return normalizeToolCallResp(resp, origSchemas, logger)
		}
	}
}

// simplifySchema recursively removes strict constraints that trip up weaker models.
func simplifySchema(schema map[string]any) {
	delete(schema, "additionalProperties")

	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)

	// Track which fields are nullable so we can remove them from required.
	nullable := map[string]bool{}

	for name, prop := range props {
		p, ok := prop.(map[string]any)
		if !ok {
			continue
		}

		// Convert union types ["type", "null"] → just "type".
		if types, ok := p["type"].([]any); ok {
			for _, t := range types {
				if ts, ok := t.(string); ok && ts != "null" {
					p["type"] = ts
					break
				}
			}
			nullable[name] = true
		}

		delete(p, "additionalProperties")
		delete(p, "minItems")
		delete(p, "maxItems")

		// Recurse into nested items.
		if items, ok := p["items"].(map[string]any); ok {
			simplifySchema(items)
		}
	}

	// Remove nullable fields from required.
	if len(nullable) > 0 && len(required) > 0 {
		var kept []any
		for _, r := range required {
			rs, _ := r.(string)
			if !nullable[rs] {
				kept = append(kept, r)
			}
		}
		schema["required"] = kept
	}
}

// --- original schema tracking ---

type origSchema struct {
	// required fields and their types from the original schema
	requiredNullable map[string]bool // field name → was it nullable-required?
}

func buildOrigSchemas(tools []any) map[string]*origSchema {
	schemas := map[string]*origSchema{}
	for _, tool := range tools {
		t, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := t["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		params, _ := fn["parameters"].(map[string]any)
		if name == "" || params == nil {
			continue
		}

		os := &origSchema{requiredNullable: map[string]bool{}}
		findNullableRequired(params, "", os)
		schemas[name] = os
	}
	return schemas
}

func findNullableRequired(schema map[string]any, prefix string, os *origSchema) {
	props, _ := schema["properties"].(map[string]any)
	required := toStringSlice(schema["required"])
	reqSet := map[string]bool{}
	for _, r := range required {
		reqSet[r] = true
	}

	for name, prop := range props {
		p, ok := prop.(map[string]any)
		if !ok {
			continue
		}
		fullName := name
		if prefix != "" {
			fullName = prefix + "." + name
		}
		if _, isArr := p["type"].([]any); isArr && reqSet[name] {
			os.requiredNullable[fullName] = true
		}
		if items, ok := p["items"].(map[string]any); ok {
			findNullableRequired(items, fullName, os)
		}
	}
}

// --- response normalization ---

func normalizeToolCallResp(resp *http.Response, schemas map[string]*origSchema, logger *slog.Logger) (*http.Response, error) {
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		return resp, nil
	}

	var chatResp map[string]any
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		return resp, nil
	}

	choices, _ := chatResp["choices"].([]any)
	modified := false

	for _, choice := range choices {
		c, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := c["message"].(map[string]any)
		if !ok {
			continue
		}
		toolCalls, ok := msg["tool_calls"].([]any)
		if !ok {
			continue
		}

		for _, tc := range toolCalls {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tcMap["function"].(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			argsStr, _ := fn["arguments"].(string)

			fixed := fixArguments(name, argsStr, schemas, logger)
			if fixed != argsStr {
				fn["arguments"] = fixed
				modified = true
			}
		}
	}

	if modified {
		respBody, _ = json.Marshal(chatResp)
		logger.Debug("tool_adapter: normalized tool_call arguments")
	}

	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	resp.ContentLength = int64(len(respBody))
	return resp, nil
}

func fixArguments(toolName, argsStr string, schemas map[string]*origSchema, logger *slog.Logger) string {
	if argsStr == "" || argsStr == "{}" {
		return argsStr
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		return argsStr
	}

	os := schemas[toolName]
	if os == nil {
		return argsStr
	}

	changed := false

	// Fix string-encoded arrays: model returns "files": "[{...}]" instead of [{...}]
	for key, val := range args {
		str, ok := val.(string)
		if !ok {
			continue
		}
		str = strings.TrimSpace(str)
		if (strings.HasPrefix(str, "[") && strings.HasSuffix(str, "]")) ||
			(strings.HasPrefix(str, "{") && strings.HasSuffix(str, "}")) {
			var parsed any
			if json.Unmarshal([]byte(str), &parsed) == nil {
				args[key] = parsed
				changed = true
				logger.Debug("tool_adapter: unpacked string-encoded value",
					"tool", toolName, "field", key)
			}
		}
	}

	// Add missing nullable-required fields at top level.
	for field := range os.requiredNullable {
		if !strings.Contains(field, ".") {
			if _, exists := args[field]; !exists {
				args[field] = nil
				changed = true
			}
		}
	}

	// For array items: add missing nullable-required fields inside each item.
	for key, val := range args {
		arr, ok := val.([]any)
		if !ok {
			continue
		}
		for _, item := range arr {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for field := range os.requiredNullable {
				parts := strings.SplitN(field, ".", 2)
				if len(parts) == 2 && parts[0] == key {
					if _, exists := itemMap[parts[1]]; !exists {
						itemMap[parts[1]] = nil
						changed = true
					}
				}
			}
		}
	}

	if !changed {
		return argsStr
	}

	fixed, err := json.Marshal(args)
	if err != nil {
		return argsStr
	}
	return string(fixed)
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
