package httpproxy

import (
	"context"
	"net/http"
)

// ProxyRequest is the unit of work flowing through the middleware pipeline.
// It wraps the original HTTP request and captures the upstream response.
type ProxyRequest struct {
	HTTP     *http.Request
	Model    string // extracted from request body for logging/routing
	Stream   bool   // true if client requested streaming
	BodyRaw  []byte // buffered request body (for inspection/modification)
}

type Handler func(ctx context.Context, req *ProxyRequest) (*http.Response, error)

type Middleware func(next Handler) Handler

type Predicate func(req *ProxyRequest) bool

func Pipeline(final Handler, mws ...Middleware) Handler {
	h := final
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func Compose(mws ...Middleware) Middleware {
	return func(next Handler) Handler {
		return Pipeline(next, mws...)
	}
}

func When(pred Predicate, mw Middleware) Middleware {
	return func(next Handler) Handler {
		wrapped := mw(next)
		return func(ctx context.Context, req *ProxyRequest) (*http.Response, error) {
			if pred(req) {
				return wrapped(ctx, req)
			}
			return next(ctx, req)
		}
	}
}

func Passthrough() Middleware {
	return func(next Handler) Handler {
		return next
	}
}
