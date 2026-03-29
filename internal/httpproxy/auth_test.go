package httpproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"aibroker/internal/config"
)

func TestMergeSplitAPIKey(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer alpha")
	r.Header.Set("X-Suf", "beta")
	key, err := mergeSplitAPIKey(r, "bearer_first", "X-Suf")
	if err != nil || key != "alphabeta" {
		t.Fatalf("bearer_first: got %q err %v", key, err)
	}
	r2 := httptest.NewRequest(http.MethodPost, "/", nil)
	r2.Header.Set("Authorization", "Bearer alpha")
	r2.Header.Set("X-Suf", "beta")
	key2, err := mergeSplitAPIKey(r2, "header_first", "X-Suf")
	if err != nil || key2 != "betaalpha" {
		t.Fatalf("header_first: got %q err %v", key2, err)
	}
}

func TestResolveSplitExtraHeader(t *testing.T) {
	auth := &config.AuthFromClientConfig{Enabled: true, ExtraHeader: "X-Global"}
	routing := &config.ClientRoutingConfig{
		Continue: config.ContinueRoutingConfig{ExtraHeader: "X-Cont"},
	}
	if got := resolveSplitExtraHeader(ClientContinue, auth, routing); got != "X-Cont" {
		t.Fatalf("Continue override: got %q", got)
	}
	if got := resolveSplitExtraHeader(ClientOpenAICompat, auth, routing); got != "X-Global" {
		t.Fatalf("OpenAI compat: got %q", got)
	}
}

func TestColonBearerSplitContinue(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer sk-openai:proj-abc-123")
	authz, extra, err := colonBearerSplitContinue(r, "X-Continue-Id")
	if err != nil {
		t.Fatal(err)
	}
	if authz != "Bearer sk-openai" {
		t.Fatalf("authz: got %q", authz)
	}
	if extra["X-Continue-Id"] != "proj-abc-123" {
		t.Fatalf("id header: %v", extra)
	}
	r2 := httptest.NewRequest(http.MethodPost, "/", nil)
	r2.Header.Set("Authorization", "Bearer nocolon")
	if _, _, err := colonBearerSplitContinue(r2, "X-Id"); err == nil {
		t.Fatal("expected error without colon")
	}
}
