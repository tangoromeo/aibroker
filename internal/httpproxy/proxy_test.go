package httpproxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-123",
			"object": "chat.completion",
			"model":  json.RawMessage(body),
		})
	}))
	defer upstream.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(Config{Upstream: upstream.URL}, Logging(logger), logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["id"] != "chatcmpl-123" {
		t.Fatalf("unexpected response: %v", resp)
	}
}

func TestProxyPassesAuthHeader(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(Config{Upstream: upstream.URL}, nil, logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"test"}`))
	req.Header.Set("Authorization", "Bearer client-key-123")

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if gotAuth != "Bearer client-key-123" {
		t.Fatalf("expected client auth header, got %q", gotAuth)
	}
}

func TestProxyOverridesAPIKey(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(Config{Upstream: upstream.URL, APIKey: "sk-proxy-managed"}, nil, logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"test"}`))
	req.Header.Set("Authorization", "Bearer client-key")

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if gotAuth != "Bearer sk-proxy-managed" {
		t.Fatalf("expected proxy key, got %q", gotAuth)
	}
}

func TestProxyStreamSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"chunk\":1}\n\ndata: {\"chunk\":2}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(Config{Upstream: upstream.URL}, nil, logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"test","stream":true}`))

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("expected [DONE] in stream, got %q", w.Body.String())
	}
}

func TestProxyHealth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(Config{Upstream: "http://unused"}, nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestProxyMiddlewareModifiesRequest(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	rewriter := func(next Handler) Handler {
		return func(ctx context.Context, req *ProxyRequest) (*http.Response, error) {
			req.BodyRaw = []byte(`{"model":"rewritten"}`)
			return next(ctx, req)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(Config{Upstream: upstream.URL}, Middleware(rewriter), logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"original"}`))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if gotBody != `{"model":"rewritten"}` {
		t.Fatalf("expected rewritten body, got %q", gotBody)
	}
}
