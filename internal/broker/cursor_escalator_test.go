package broker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractUserPromptFromShapedBody(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"hello"},{"role":"user","content":"world"}]}`)
	s, err := extractUserPromptFromShapedBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "hello") || !strings.Contains(s, "world") {
		t.Fatal(s)
	}
}

func TestBuildOpenAIChatCompletionJSON(t *testing.T) {
	b := buildOpenAIChatCompletionJSON("default", "hi")
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["object"] != "chat.completion" {
		t.Fatal(out)
	}
}
