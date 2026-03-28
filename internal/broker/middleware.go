package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"aibroker/internal/httpproxy"
)

// DetectMiddleware analyzes conversation history.
// If failures detected — sets EscalationState in context. Otherwise passthrough.
func DetectMiddleware(detector FailureDetector, logger *slog.Logger) httpproxy.Middleware {
	return func(next httpproxy.Handler) httpproxy.Handler {
		return func(ctx context.Context, req *httpproxy.ProxyRequest) (*http.Response, error) {
			if !strings.HasSuffix(req.HTTP.URL.Path, "/chat/completions") {
				return next(ctx, req)
			}

			var chatReq ChatRequest
			if err := json.Unmarshal(req.BodyRaw, &chatReq); err != nil {
				return next(ctx, req)
			}

			signal := detector.Analyze(&chatReq)
			if !signal.ShouldEscalate {
				return next(ctx, req)
			}

			logger.Warn("escalation triggered",
				"reason", signal.Reason,
				"pattern", signal.Pattern,
				"failures", signal.FailureCount,
			)

			state := &EscalationState{
				Triggered: true,
				Signal:    signal,
			}
			ctx = withState(ctx, state)
			// Stash parsed request for downstream steps.
			ctx = context.WithValue(ctx, chatReqKey{}, &chatReq)
			return next(ctx, req)
		}
	}
}

// ScreenMiddleware evaluates security policies via LLM.
// Screens the SHAPED content (what would actually leave the perimeter),
// not the raw request. Requires ShapeMiddleware to run first.
func ScreenMiddleware(policy PolicyEngine, logger *slog.Logger) httpproxy.Middleware {
	return func(next httpproxy.Handler) httpproxy.Handler {
		return func(ctx context.Context, req *httpproxy.ProxyRequest) (*http.Response, error) {
			state := getState(ctx)
			if state == nil || !state.Triggered || state.Shaped == nil {
				return next(ctx, req)
			}

			// Screen the shaped body — this is what would actually go external.
			var shapedReq ChatRequest
			if err := json.Unmarshal(state.Shaped.Body, &shapedReq); err != nil {
				logger.Error("screen: can't parse shaped body", "err", err)
				state.Triggered = false
				return next(ctx, req)
			}

			perm, err := policy.Evaluate(ctx, &shapedReq)
			if err != nil {
				logger.Error("screen failed", "err", err)
				state.Triggered = false
				return next(ctx, req)
			}
			if !perm.Allowed {
				logger.Warn("screen: blocked", "reason", perm.Reason)
				state.Triggered = false
				return next(ctx, req)
			}

			logger.Info("screen: passed", "findings", len(perm.Findings))
			state.Permission = perm
			return next(ctx, req)
		}
	}
}

// ShapeMiddleware minimizes context via LLM for the external model.
// Stores shaped body in EscalationState, does NOT modify the original request.
func ShapeMiddleware(shaper ContextShaper, targetModel string, logger *slog.Logger) httpproxy.Middleware {
	return func(next httpproxy.Handler) httpproxy.Handler {
		return func(ctx context.Context, req *httpproxy.ProxyRequest) (*http.Response, error) {
			state := getState(ctx)
			if state == nil || !state.Triggered {
				return next(ctx, req)
			}

			chatReq := getChatReq(ctx)
			shaped, err := shaper.Shape(ctx, chatReq, state.Permission, targetModel)
			if err != nil {
				logger.Error("shape failed", "err", err)
				state.Triggered = false
				return next(ctx, req)
			}

			logger.Info("shape: done",
				"summary", truncStr(shaped.Summary, 120),
				"tokens_est", shaped.TokenEstimate,
			)
			state.Shaped = shaped
			return next(ctx, req)
		}
	}
}

// ForwardMiddleware sends shaped request to the external model (or stub).
// If escalation is active, screened, and shaped — calls escalator and returns response.
// Otherwise passes through to next (local model).
func ForwardMiddleware(escalator Escalator, validator ResponseValidator, originalModel string, logger *slog.Logger) httpproxy.Middleware {
	return func(next httpproxy.Handler) httpproxy.Handler {
		return func(ctx context.Context, req *httpproxy.ProxyRequest) (*http.Response, error) {
			state := getState(ctx)
			if state == nil || !state.Triggered || state.Shaped == nil || state.Permission == nil {
				return next(ctx, req)
			}

			respBody, err := escalator.Forward(ctx, state.Shaped.Body)
			if err != nil {
				logger.Error("escalate failed", "err", err)
				return next(ctx, req)
			}

			vr, _ := validator.Validate(ctx, respBody)
			if vr != nil && !vr.Valid {
				logger.Warn("validate failed", "reason", vr.Reason)
				return next(ctx, req)
			}

			logger.Info("escalation complete", "response_bytes", len(respBody))
			return syntheticResponse(respBody, originalModel, req.Stream), nil
		}
	}
}

// --- context helpers for passing parsed ChatRequest ---

type chatReqKey struct{}

func getChatReq(ctx context.Context) *ChatRequest {
	if r, ok := ctx.Value(chatReqKey{}).(*ChatRequest); ok {
		return r
	}
	return nil
}
