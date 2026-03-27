package httpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Proxy struct {
	upstream   string
	apiKey     string
	headers    map[string]string
	client     *http.Client
	pipeline   Middleware
	logger     *slog.Logger
}

type Config struct {
	Upstream   string
	APIKey     string
	Timeout    time.Duration
	Headers    map[string]string // extra headers to add to upstream requests
	HTTPClient *http.Client      // optional pre-configured client (for custom TLS)
}

func New(cfg Config, pipeline Middleware, logger *slog.Logger) *Proxy {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	} else {
		client.Timeout = cfg.Timeout
	}
	return &Proxy{
		upstream: strings.TrimRight(cfg.Upstream, "/"),
		apiKey:   cfg.APIKey,
		headers:  cfg.Headers,
		client:   client,
		pipeline: pipeline,
		logger:   logger,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/health" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	model, stream := extractMeta(body)

	preq := &ProxyRequest{
		HTTP:    r,
		Model:   model,
		Stream:  stream,
		BodyRaw: body,
	}

	h := p.forwardHandler()
	if p.pipeline != nil {
		h = p.pipeline(h)
	}

	resp, err := h(r.Context(), preq)
	if err != nil {
		p.logger.Error("pipeline error", "path", r.URL.Path, "err", err)
		p.writeError(w, http.StatusBadGateway, "upstream error")
		return
	}
	defer resp.Body.Close()

	p.copyResponse(w, resp, preq.Stream)
}

func (p *Proxy) forwardHandler() Handler {
	return func(ctx context.Context, req *ProxyRequest) (*http.Response, error) {
		upURL := p.upstream + req.HTTP.URL.Path
		if req.HTTP.URL.RawQuery != "" {
			upURL += "?" + req.HTTP.URL.RawQuery
		}

		upReq, err := http.NewRequestWithContext(ctx, req.HTTP.Method, upURL, bytes.NewReader(req.BodyRaw))
		if err != nil {
			return nil, fmt.Errorf("create upstream request: %w", err)
		}

		for k, vv := range req.HTTP.Header {
			kl := strings.ToLower(k)
			if kl == "host" || kl == "connection" {
				continue
			}
			for _, v := range vv {
				upReq.Header.Add(k, v)
			}
		}

		if p.apiKey != "" {
			upReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}

		for k, v := range p.headers {
			upReq.Header.Set(k, v)
		}

		upReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(req.BodyRaw)))

		return p.client.Do(upReq)
	}
}

func (p *Proxy) copyResponse(w http.ResponseWriter, resp *http.Response, streamRequested bool) {
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if streamRequested || isSSE(resp) {
		p.streamSSE(w, resp.Body)
	} else {
		_, _ = io.Copy(w, resp.Body)
	}
}

func (p *Proxy) streamSSE(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if ok {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Proxy) writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "proxy_error",
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func extractMeta(body []byte) (model string, stream bool) {
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	return req.Model, req.Stream
}

func isSSE(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "text/event-stream")
}
