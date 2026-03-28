package httpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// RequestDump saves full request bodies to files for debugging.
// Files are written to the specified directory as request_N.json.
// It also wraps the response body with a TeeReader to capture
// the exact bytes sent back to the client (response_N.sse).
func RequestDump(dir string, logger *slog.Logger) Middleware {
	_ = os.MkdirAll(dir, 0o755)
	var seq atomic.Int64

	return func(next Handler) Handler {
		return func(ctx context.Context, req *ProxyRequest) (*http.Response, error) {
			n := seq.Add(1)

			reqPath := filepath.Join(dir, fmt.Sprintf("request_%d.json", n))
			if err := os.WriteFile(reqPath, req.BodyRaw, 0o644); err != nil {
				logger.Error("dump request", "err", err)
			} else {
				logger.Info("request dumped", "path", reqPath, "bytes", len(req.BodyRaw))
			}

			meta := dumpMeta{
				Seq:       n,
				Path:      req.HTTP.URL.Path,
				Method:    req.HTTP.Method,
				Model:     req.Model,
				Stream:    req.Stream,
				BodyBytes: len(req.BodyRaw),
				// Broker shape/screen/forward only run for chat completions.
				BrokerEscalationPipeline: strings.HasSuffix(req.HTTP.URL.Path, "/chat/completions"),
			}
			if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
				_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("request_%d.meta.json", n)), b, 0o644)
			}

			resp, err := next(ctx, req)
			if err != nil {
				return nil, err
			}

			respPath := filepath.Join(dir, fmt.Sprintf("response_%d.sse", n))
			f, ferr := os.Create(respPath)
			if ferr != nil {
				logger.Error("create response dump", "err", ferr)
				return resp, nil
			}
			logger.Info("response dump", "path", respPath)

			orig := resp.Body
			resp.Body = &teeReadCloser{
				Reader: io.TeeReader(orig, f),
				closeFn: func() error {
					f.Close()
					return orig.Close()
				},
			}

			return resp, nil
		}
	}
}

type teeReadCloser struct {
	io.Reader
	closeFn func() error
}

func (t *teeReadCloser) Close() error {
	return t.closeFn()
}

// dumpMeta explains what was captured; BrokerEscalationPipeline=false means
// shape/screen/forward skipped this request (not a chat completion).
type dumpMeta struct {
	Seq                      int64  `json:"seq"`
	Path                     string `json:"path"`
	Method                   string `json:"method"`
	Model                    string `json:"model,omitempty"`
	Stream                   bool   `json:"stream"`
	BodyBytes                int    `json:"body_bytes"`
	BrokerEscalationPipeline bool   `json:"broker_escalation_pipeline"`
}
