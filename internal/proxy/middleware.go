package proxy

import (
	"context"

	"aibroker/internal/jsonrpc"
)

type Handler func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error)

type Middleware func(next Handler) Handler

type Predicate func(msg *jsonrpc.Message) bool

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
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			if pred(msg) {
				return wrapped(ctx, msg)
			}
			return next(ctx, msg)
		}
	}
}

func Branch(pred Predicate, ifTrue, ifFalse Middleware) Middleware {
	return func(next Handler) Handler {
		trueH := ifTrue(next)
		falseH := ifFalse(next)
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			if pred(msg) {
				return trueH(ctx, msg)
			}
			return falseH(ctx, msg)
		}
	}
}

func Router(classify func(*jsonrpc.Message) string, routes map[string]Middleware, fallback Middleware) Middleware {
	return func(next Handler) Handler {
		built := make(map[string]Handler, len(routes))
		for k, mw := range routes {
			built[k] = mw(next)
		}
		var fb Handler
		if fallback != nil {
			fb = fallback(next)
		}
		return func(ctx context.Context, msg *jsonrpc.Message) (*jsonrpc.Message, error) {
			key := classify(msg)
			if h, ok := built[key]; ok {
				return h(ctx, msg)
			}
			if fb != nil {
				return fb(ctx, msg)
			}
			return next(ctx, msg)
		}
	}
}

func Passthrough() Middleware {
	return func(next Handler) Handler {
		return next
	}
}
