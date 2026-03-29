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

	"aibroker/internal/config"
)

type Proxy struct {
	upstream       string
	apiKey         string
	authFromClient *config.AuthFromClientConfig
	clientRouting  *config.ClientRoutingConfig
	headers        map[string]string
	client         *http.Client
	pipeline       Middleware
	logger         *slog.Logger
}

type Config struct {
	Upstream       string
	APIKey         string
	AuthFromClient *config.AuthFromClientConfig
	ClientRouting  *config.ClientRoutingConfig
	Timeout        time.Duration
	Headers        map[string]string // extra headers to add to upstream requests
	HTTPClient     *http.Client      // optional pre-configured client (for custom TLS)
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
		upstream:       strings.TrimRight(cfg.Upstream, "/"),
		apiKey:         cfg.APIKey,
		authFromClient: cfg.AuthFromClient,
		clientRouting:  cfg.ClientRouting,
		headers:        cfg.Headers,
		client:         client,
		pipeline:       pipeline,
		logger:         logger,
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

		authz, strip, extraAuth, err := p.resolveUpstreamAuth(req.HTTP)
		if err != nil {
			return unauthorizedResponse(err.Error()), nil
		}
		if authz != "" {
			upReq.Header.Set("Authorization", authz)
			for _, h := range strip {
				upReq.Header.Del(h)
			}
		}
		for k, v := range extraAuth {
			upReq.Header.Set(k, v)
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

func (p *Proxy) resolveUpstreamAuth(r *http.Request) (authorization string, strip []string, extra map[string]string, err error) {
	if p.authFromClient != nil && p.authFromClient.Enabled {
		kind := DetectClientKind(r, p.clientRouting)
		if kind == ClientContinue && p.clientRouting != nil &&
			p.clientRouting.Continue.ColonBearerSplit != nil && p.clientRouting.Continue.ColonBearerSplit.Enabled {
			authz, extraH, err := colonBearerSplitContinue(r, p.clientRouting.Continue.ColonBearerSplit.IDHeader)
			if err != nil {
				return "", nil, nil, err
			}
			return authz, nil, extraH, nil
		}
		extraHdr := resolveSplitExtraHeader(kind, p.authFromClient, p.clientRouting)
		if extraHdr == "" {
			return "", nil, nil, fmt.Errorf("auth_from_client: extra_header not configured for OpenAI-compatible clients (Kilo, etc.); set upstream.auth_from_client.extra_header or route only Continue with colon_bearer_split")
		}
		key, err := mergeSplitAPIKey(r, p.authFromClient.Join, extraHdr)
		if err != nil {
			return "", nil, nil, err
		}
		return "Bearer " + key, []string{extraHdr}, nil, nil
	}
	if p.apiKey != "" {
		return "Bearer " + p.apiKey, nil, nil, nil
	}
	return "", nil, nil, nil
}

func unauthorizedResponse(msg string) *http.Response {
	body := fmt.Sprintf(`{"error":{"message":%q,"type":"authentication_error"}}`, msg)
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
