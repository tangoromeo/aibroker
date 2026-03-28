package httpproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeToolCallSSE(t *testing.T) {
	// Exact SSE stream from the production dump: read_file with string-encoded files array.
	sseBody := strings.Join([]string{
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1774679402,"model":"Qwen","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_abc","type":"function","index":0,"function":{"name":"read_file","arguments":""}}]},"logprobs":null,"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1774679402,"model":"Qwen","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]},"logprobs":null,"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1774679402,"model":"Qwen","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"files\": \"[{\\\"path\\\": \\\"configs/aibroker.yaml\\\"}]\""}}]},"logprobs":null,"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1774679402,"model":"Qwen","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"logprobs":null,"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1774679402,"model":"Qwen","choices":[{"index":0,"delta":{"content":""},"logprobs":null,"finish_reason":"tool_calls","stop_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1774679402,"model":"Qwen","choices":[],"usage":{"prompt_tokens":7717,"total_tokens":7828,"completion_tokens":111}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	schemas := map[string]*origSchema{
		"read_file": {requiredNullable: map[string]bool{"files.line_ranges": true}},
	}

	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fixed := normalizeToolCallSSE(resp, schemas, logger)

	body, err := io.ReadAll(fixed.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	output := string(body)
	t.Logf("Output:\n%s", output)

	// Must contain [DONE] exactly once.
	if c := strings.Count(output, "data: [DONE]"); c != 1 {
		t.Errorf("expected 1 [DONE], got %d", c)
	}

	// Find the corrected tool_call chunk.
	scanner := bufio.NewScanner(strings.NewReader(output))
	foundFixed := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		tcs, _ := delta["tool_calls"].([]any)
		if len(tcs) == 0 {
			continue
		}
		tc, _ := tcs[0].(map[string]any)
		fn, _ := tc["function"].(map[string]any)
		if fn == nil {
			continue
		}
		args, _ := fn["arguments"].(string)
		if args == "" {
			continue
		}

		t.Logf("Corrected arguments: %s", args)

		// Parse and verify the arguments.
		var parsed map[string]any
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			t.Errorf("arguments not valid JSON: %v", err)
			continue
		}

		// files must be an array, not a string.
		files, ok := parsed["files"].([]any)
		if !ok {
			t.Errorf("files is not an array: %T = %v", parsed["files"], parsed["files"])
			continue
		}
		if len(files) != 1 {
			t.Errorf("expected 1 file, got %d", len(files))
			continue
		}

		f, _ := files[0].(map[string]any)
		path, _ := f["path"].(string)
		if path != "configs/aibroker.yaml" {
			t.Errorf("expected path 'configs/aibroker.yaml', got %q", path)
		}

		// line_ranges should be added as null (nullable-required).
		if _, exists := f["line_ranges"]; !exists {
			t.Errorf("missing line_ranges field (should be null)")
		}

		foundFixed = true
	}

	if !foundFixed {
		t.Error("did not find corrected tool_call chunk in output")
	}

	// Must not contain string-encoded array.
	if bytes.Contains(body, []byte(`"[{`)) {
		t.Error("output still contains string-encoded array")
	}
}
