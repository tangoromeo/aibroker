package httpproxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

func Logging(logger *slog.Logger) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req *ProxyRequest) (*http.Response, error) {
			start := time.Now()

			logger.Info("request",
				"method", req.HTTP.Method,
				"path", req.HTTP.URL.Path,
				"model", req.Model,
				"stream", req.Stream,
				"body_bytes", len(req.BodyRaw),
			)
			logger.Debug("request_body", "body", truncate(req.BodyRaw, 4096))

			resp, err := next(ctx, req)
			dur := time.Since(start)

			if err != nil {
				logger.Error("upstream_error",
					"path", req.HTTP.URL.Path,
					"model", req.Model,
					"duration", dur,
					"err", err,
				)
				return nil, err
			}

			logger.Info("response",
				"path", req.HTTP.URL.Path,
				"model", req.Model,
				"status", resp.StatusCode,
				"duration", dur,
				"stream", req.Stream || isSSE(resp),
			)

			// Only capture response body for non-streaming responses.
			// Buffering a stream breaks real-time delivery to clients.
			if logger.Enabled(ctx, slog.LevelDebug) && !req.Stream && !isSSE(resp) {
				respBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				logger.Debug("response_body", "body", truncate(respBody, 4096))
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
			}

			return resp, nil
		}
	}
}

func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}
