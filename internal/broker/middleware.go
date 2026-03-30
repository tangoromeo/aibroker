package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"aibroker/internal/httpproxy"
)

// DetectMiddleware analyzes conversation history.
// If failures detected — sets EscalationState in context. Otherwise passthrough.
func DetectMiddleware(detector FailureDetector, logger *slog.Logger) httpproxy.Middleware {
	var cycleSeq atomic.Uint64
	return func(next httpproxy.Handler) httpproxy.Handler {
		return func(ctx context.Context, req *httpproxy.ProxyRequest) (*http.Response, error) {
			if !strings.HasSuffix(req.HTTP.URL.Path, "/chat/completions") {
				return next(ctx, req)
			}

			var chatReq ChatRequest
			if err := json.Unmarshal(req.BodyRaw, &chatReq); err != nil {
				logger.Info("escalation.detect: skip invalid chat payload",
					"path", req.HTTP.URL.Path,
					"err", err,
				)
				return next(ctx, req)
			}
			cycleID := req.HTTP.Header.Get("X-Request-Id")
			if cycleID == "" {
				cycleID = "esc-" + strconv.FormatUint(cycleSeq.Add(1), 10)
			}
			logger.Info("escalation.detect: start",
				"cycle_id", cycleID,
				"path", req.HTTP.URL.Path,
				"model", chatReq.Model,
				"messages", len(chatReq.Messages),
				"stream", req.Stream,
			)

			signal := detector.Analyze(&chatReq)
			if !signal.ShouldEscalate {
				logger.Info("escalation.detect: pass local",
					"cycle_id", cycleID,
					"reason", signal.Reason,
					"pattern", signal.Pattern,
					"failures", signal.FailureCount,
				)
				return next(ctx, req)
			}

			logger.Info("escalation.detect: triggered",
				"cycle_id", cycleID,
				"reason", signal.Reason,
				"pattern", signal.Pattern,
				"failures", signal.FailureCount,
			)

			state := &EscalationState{
				CycleID:   cycleID,
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
			logger.Info("escalation.screen: start",
				"cycle_id", state.CycleID,
				"shaped_bytes", len(state.Shaped.Body),
			)

			// Screen the shaped body — this is what would actually go external.
			var shapedReq ChatRequest
			if err := json.Unmarshal(state.Shaped.Body, &shapedReq); err != nil {
				logger.Error("screen: can't parse shaped body", "err", err)
				logger.Info("escalation.screen: disabled due to parse error",
					"cycle_id", state.CycleID,
				)
				state.Triggered = false
				return next(ctx, req)
			}

			perm, err := policy.Evaluate(ctx, &shapedReq)
			if err != nil {
				logger.Error("screen failed", "err", err)
				logger.Info("escalation.screen: disabled due to screening error",
					"cycle_id", state.CycleID,
				)
				state.Triggered = false
				return next(ctx, req)
			}
			if !perm.Allowed {
				logger.Warn("screen: blocked", "reason", perm.Reason)
				logger.Info("escalation.screen: blocked",
					"cycle_id", state.CycleID,
					"reason", perm.Reason,
					"findings", len(perm.Findings),
				)
				state.Triggered = false
				return next(ctx, req)
			}

			logger.Info("escalation.screen: passed",
				"cycle_id", state.CycleID,
				"findings", len(perm.Findings),
			)
			state.Permission = perm
			return next(ctx, req)
		}
	}
}

// ShapeMiddleware minimizes context via LLM for the external model.
// Stores shaped body in EscalationState, does NOT modify the original request.
// If DetectMiddleware is absent from the pipeline, Shape creates the state itself.
// Always dumps shaped body to dumpDir for visibility.
func ShapeMiddleware(shaper ContextShaper, targetModel string, dumpDir string, logger *slog.Logger) httpproxy.Middleware {
	if dumpDir != "" {
		_ = os.MkdirAll(dumpDir, 0o755)
	}
	var seq atomic.Int64
	return func(next httpproxy.Handler) httpproxy.Handler {
		return func(ctx context.Context, req *httpproxy.ProxyRequest) (*http.Response, error) {
			if !strings.HasSuffix(req.HTTP.URL.Path, "/chat/completions") {
				return next(ctx, req)
			}

			state := getState(ctx)
			if state == nil {
				// No detect step in pipeline — create state ourselves.
				state = &EscalationState{CycleID: "esc-direct", Triggered: true, Signal: EscalationSignal{
					ShouldEscalate: true, Reason: "no detect in pipeline", Pattern: "direct",
				}}
				ctx = withState(ctx, state)
				logger.Info("escalation.shape: detect stage missing, force direct mode",
					"cycle_id", state.CycleID,
				)
			}
			if !state.Triggered {
				return next(ctx, req)
			}

			chatReq := getChatReq(ctx)
			if chatReq == nil {
				var cr ChatRequest
				if err := json.Unmarshal(req.BodyRaw, &cr); err != nil {
					return next(ctx, req)
				}
				chatReq = &cr
				ctx = context.WithValue(ctx, chatReqKey{}, chatReq)
			}
			logger.Info("escalation.shape: start",
				"cycle_id", state.CycleID,
				"target_model", targetModel,
				"messages", len(chatReq.Messages),
			)

			shaped, err := shaper.Shape(ctx, chatReq, state.Permission, targetModel)
			if err != nil {
				logger.Error("shape failed", "err", err)
				logger.Info("escalation.shape: disabled due to shaping error",
					"cycle_id", state.CycleID,
				)
				state.Triggered = false
				return next(ctx, req)
			}

			logger.Info("escalation.shape: done",
				"cycle_id", state.CycleID,
				"summary", truncStr(shaped.Summary, 120),
				"tokens_est", shaped.TokenEstimate,
				"shaped_bytes", len(shaped.Body),
			)
			state.Shaped = shaped

			if dumpDir != "" {
				n := seq.Add(1)
				path := filepath.Join(dumpDir, fmt.Sprintf("shaped_%d.json", n))
				if err := os.WriteFile(path, shaped.Body, 0o644); err == nil {
					logger.Info("escalation.shape: dumped shaped body",
						"cycle_id", state.CycleID,
						"path", path,
					)
				}
				meta := map[string]any{
					"seq":    n,
					"path":   req.HTTP.URL.Path,
					"note":   "shaped body sent to screening; only produced for /chat/completions",
					"summary": truncStr(shaped.Summary, 200),
				}
				if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
					_ = os.WriteFile(filepath.Join(dumpDir, fmt.Sprintf("shaped_%d.meta.json", n)), b, 0o644)
				}
			}

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
			logger.Info("escalation.forward: start",
				"cycle_id", state.CycleID,
				"original_model", originalModel,
				"stream", req.Stream,
				"shaped_bytes", len(state.Shaped.Body),
			)

			respBody, err := escalator.Forward(ctx, state.Shaped.Body)
			if err != nil {
				logger.Error("escalate failed", "err", err)
				logger.Info("escalation.forward: fallback to local due to escalator error",
					"cycle_id", state.CycleID,
				)
				return next(ctx, req)
			}

			vr, _ := validator.Validate(ctx, respBody)
			if vr != nil && !vr.Valid {
				logger.Warn("validate failed", "reason", vr.Reason)
				logger.Info("escalation.forward: fallback to local due to response validation",
					"cycle_id", state.CycleID,
					"reason", vr.Reason,
				)
				return next(ctx, req)
			}

			logger.Info("escalation.forward: completed",
				"cycle_id", state.CycleID,
				"response_bytes", len(respBody),
				"escalated", true,
			)
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
