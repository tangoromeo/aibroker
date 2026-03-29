package httpproxy

import (
	"net/http/httptest"
	"testing"

	"aibroker/internal/config"
)

func TestDetectClientKind(t *testing.T) {
	cr := &config.ClientRoutingConfig{
		Continue: config.ContinueRoutingConfig{
			UserAgentSubstrings: []string{"Continue/"},
			HeaderPresent:       "X-Continue-Plugin",
		},
	}
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if DetectClientKind(req, cr) != ClientOpenAICompat {
		t.Fatal("expected OpenAI compat")
	}
	req2 := httptest.NewRequest("POST", "/", nil)
	req2.Header.Set("User-Agent", "Continue/0.9 vscode")
	if DetectClientKind(req2, cr) != ClientContinue {
		t.Fatal("expected Continue via UA")
	}
	req3 := httptest.NewRequest("POST", "/", nil)
	req3.Header.Set("X-Continue-Plugin", "1")
	if DetectClientKind(req3, cr) != ClientContinue {
		t.Fatal("expected Continue via header")
	}
	if DetectClientKind(req, nil) != ClientOpenAICompat {
		t.Fatal("nil routing => openai compat")
	}
}
