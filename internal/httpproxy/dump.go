package httpproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
