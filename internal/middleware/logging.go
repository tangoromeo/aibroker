package middleware

import (
	"context"
	"log/slog"
	"time"

	"aibroker/internal/jsonrpc"
	"aibroker/internal/proxy"
)

func Logging(logger *slog.Logger) proxy.Middleware {
	return func(next proxy.Handler) proxy.Handler {
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			start := time.Now()
			dir := proxy.Inbound
			if d, ok := proxy.DirectionFromContext(ctx); ok {
				dir = d
			}
			id := string(msg.ID)

			resp, err := next(ctx, msg)
			dur := time.Since(start)

			if err != nil {
				logger.Error("jsonrpc", "direction", dirString(dir), "method", msg.Method, "id", id, "duration", dur, "err", err)
				return nil, err
			}

			if msg.IsRequest() || msg.IsNotification() {
				attrs := []any{"direction", dirString(dir), "duration", dur}
				if msg.Method != "" {
					attrs = append(attrs, "method", msg.Method)
				}
				if len(msg.ID) > 0 {
					attrs = append(attrs, "id", id)
				}
				logger.Info("jsonrpc", attrs...)
			}

			if resp != nil && resp.IsResponse() {
				rid := string(resp.ID)
				dattrs := []any{"direction", dirString(dir), "duration", dur}
				if len(resp.ID) > 0 {
					dattrs = append(dattrs, "id", rid)
				}
				logger.Debug("jsonrpc", dattrs...)
			}

			return resp, nil
		}
	}
}

func dirString(d proxy.Direction) string {
	switch d {
	case proxy.Inbound:
		return "inbound"
	case proxy.Outbound:
		return "outbound"
	default:
		return "unknown"
	}
}
