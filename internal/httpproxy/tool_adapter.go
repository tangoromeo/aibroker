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

			var body map[string]any
			if err := json.Unmarshal(req.BodyRaw, &body); err != nil {
				return next(ctx, req)
			}

			rawTools, _ := body["tools"].([]any)
			if len(rawTools) == 0 {
				return next(ctx, req)
			}

			// Store originals before modification.
			origSchemas := buildOrigSchemas(rawTools)

			// Simplify each tool definition.
			simplified := 0
			for i, tool := range rawTools {
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
					example := generateExample(params)
					if example != "" {
						desc, _ := fn["description"].(string)
						fn["description"] = desc + "\n\nREQUIRED ARGUMENTS FORMAT: " + example
					}
					simplified++
				}
				rawTools[i] = t
			}
			body["tools"] = rawTools

			// Strip broken tool_call/tool-response pairs from history.
			// Models repeat the empty-args pattern when they see it in history.
			if messages, ok := body["messages"].([]any); ok {
				cleaned, stripped := stripBrokenToolCalls(messages, logger)
				if stripped > 0 {
					body["messages"] = cleaned
					logger.Info("tool_adapter: stripped broken tool pairs", "count", stripped)
				}
			}

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

// stripBrokenToolCalls removes assistant messages that contain tool_calls with
// empty arguments, plus the corresponding tool-response messages. This prevents
// the model from learning the "call with {}" pattern from its own history.
func stripBrokenToolCalls(messages []any, logger *slog.Logger) ([]any, int) {
	// First pass: find tool_call IDs with empty/missing arguments.
	badIDs := map[string]bool{}
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok || m["role"] != "assistant" {
			continue
		}
		toolCalls, ok := m["tool_calls"].([]any)
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
			args, _ := fn["arguments"].(string)
			args = strings.TrimSpace(args)
			if args == "" || args == "{}" || args == "null" {
				if id, ok := tcMap["id"].(string); ok {
					badIDs[id] = true
				}
			}
		}
	}

	if len(badIDs) == 0 {
		return messages, 0
	}

	// Second pass: filter out bad assistant messages and their tool responses.
	stripped := 0
	var result []any
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			result = append(result, msg)
			continue
		}
		role, _ := m["role"].(string)

		if role == "tool" {
			tcID, _ := m["tool_call_id"].(string)
			if badIDs[tcID] {
				stripped++
				continue
			}
		}

		if role == "assistant" {
			if toolCalls, ok := m["tool_calls"].([]any); ok {
				allBad := true
				for _, tc := range toolCalls {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					id, _ := tcMap["id"].(string)
					if !badIDs[id] {
						allBad = false
						break
					}
				}
				if allBad {
					stripped++
					continue
				}
			}
		}

		// Strip assistant text from escalation stubs that Kilo can't handle.
		if role == "assistant" {
			if _, hasTc := m["tool_calls"]; !hasTc {
				content := messageText(m)
				if strings.Contains(content, "[AIBroker stub]") ||
					strings.Contains(content, "[AIBroker]") {
					stripped++
					continue
				}
			}
		}

		// Strip Kilo retry error messages that clutter history.
		if role == "user" {
			content := messageText(m)
			if strings.Contains(content, "without value for required parameter") ||
				strings.Contains(content, "did not provide a value for the required") ||
				strings.Contains(content, "[ERROR] You did not use a tool") {
				stripped++
				continue
			}
		}

		result = append(result, msg)
	}

	return result, stripped
}

// messageText extracts text from a message's content field.
// Handles both string content and array-of-parts (Kilo format).
func messageText(m map[string]any) string {
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, part := range c {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := p["text"].(string); ok {
				sb.WriteString(t)
				sb.WriteByte(' ')
			}
		}
		return sb.String()
	}
	return ""
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

// generateExample builds a concrete JSON example from a tool's parameter schema.
func generateExample(params map[string]any) string {
	obj := exampleObject(params)
	if len(obj) == 0 {
		return ""
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return string(b)
}

func exampleObject(schema map[string]any) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	result := make(map[string]any, len(props))
	for name, prop := range props {
		p, ok := prop.(map[string]any)
		if !ok {
			continue
		}
		result[name] = exampleValue(p)
	}
	return result
}

func exampleValue(prop map[string]any) any {
	typ, _ := prop["type"].(string)
	desc, _ := prop["description"].(string)
	descLower := strings.ToLower(desc)

	switch typ {
	case "string":
		if enums, ok := prop["enum"].([]any); ok && len(enums) > 0 {
			return enums[0]
		}
		if strings.Contains(descLower, "path") || strings.Contains(descLower, "file") || strings.Contains(descLower, "directory") {
			return "src/main.go"
		}
		if strings.Contains(descLower, "regex") || strings.Contains(descLower, "pattern") {
			return "TODO"
		}
		if strings.Contains(descLower, "mode") || strings.Contains(descLower, "slug") {
			return "code"
		}
		return "example value"
	case "boolean":
		return true
	case "integer", "number":
		return 1
	case "array":
		items, ok := prop["items"].(map[string]any)
		if !ok {
			return []any{}
		}
		return []any{exampleValue(items)}
	case "object":
		return exampleObject(prop)
	default:
		return nil
	}
}
