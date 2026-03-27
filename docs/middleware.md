# AIBroker — Middleware Pipeline Design

## Core Primitive

The entire middleware system is built on a single type:

```go
type Handler func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error)

type Middleware func(next Handler) Handler
```

Each middleware wraps the next handler, forming an "onion" — requests flow outside-in, responses inside-out:

```
Request  ──▶ [Logging] ──▶ [Screening] ──▶ [Router] ──▶ Backend
Response ◀── [Logging] ◀── [Screening] ◀── [Router] ◀── Backend
```

This gives each middleware full control:
- Inspect/modify the request before passing down
- Decide whether to call `next` at all (short-circuit)
- Inspect/modify the response coming back
- Handle errors, add timeouts, retry

## Pipeline Assembly

```go
func Pipeline(final Handler, mws ...Middleware) Handler {
    h := final
    for i := len(mws) - 1; i >= 0; i-- {
        h = mws[i](h)
    }
    return h
}
```

## Combinators

### Compose — merge multiple middleware into one

```go
func Compose(mws ...Middleware) Middleware {
    return func(next Handler) Handler {
        h := next
        for i := len(mws) - 1; i >= 0; i-- {
            h = mws[i](h)
        }
        return h
    }
}
```

### When — conditional execution

Applies middleware only when predicate matches. Otherwise passes through to next.

```go
type Predicate func(*Session, *jsonrpc.Message) bool

func When(predicate Predicate, mw Middleware) Middleware {
    return func(next Handler) Handler {
        wrapped := mw(next)
        return func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error) {
            if predicate(sess, req) {
                return wrapped(ctx, sess, req)
            }
            return next(ctx, sess, req)
        }
    }
}
```

### Branch — if/else

```go
func Branch(predicate Predicate, ifTrue, ifFalse Middleware) Middleware {
    return func(next Handler) Handler {
        trueHandler := ifTrue(next)
        falseHandler := ifFalse(next)
        return func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error) {
            if predicate(sess, req) {
                return trueHandler(ctx, sess, req)
            }
            return falseHandler(ctx, sess, req)
        }
    }
}
```

### Router — N branches by key

```go
func Router(
    classify func(*Session, *jsonrpc.Message) string,
    routes map[string]Middleware,
    fallback Middleware,
) Middleware {
    return func(next Handler) Handler {
        built := make(map[string]Handler, len(routes))
        for k, mw := range routes {
            built[k] = mw(next)
        }
        fb := fallback(next)
        return func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error) {
            key := classify(sess, req)
            if h, ok := built[key]; ok {
                return h(ctx, sess, req)
            }
            return fb(ctx, sess, req)
        }
    }
}
```

## Config-Driven Pipeline

### Three-layer approach

| Aspect | Mechanism | Latency | Why |
|--------|-----------|---------|-----|
| Pipeline structure | YAML config | — | Hot-reload without recompilation |
| Predicates/conditions | CEL expressions in YAML | ~ns | Type-safe, standard (K8s, Envoy) |
| Sync middleware logic | Go code (Registry) | ~μs | Performance, type safety |
| Async orchestration | Temporal workflows | ~ms-s | Durability, retries, human-in-the-loop |
| Security rules | OPA/Rego policies | ~μs | Hot-reload, audit, enterprise standard |

### Pipeline Configuration (YAML)

```yaml
pipeline:
  - name: logging
    middleware: logging
    config:
      level: info

  - name: classify
    middleware: classifier
    config:
      model: local

  - name: security-screening
    middleware: screening
    when: >-
      msg.method in ['session/prompt', 'tools/call']
      && size(msg.params.content) > 200
    config:
      policy: policies/code-screening.rego
      on_critical: block
      on_warning: redact

  - name: routing
    middleware: branch
    condition: "ctx.classification.local_confidence > 0.8"
    if_true:
      - name: local-forward
        middleware: forward
        config:
          backend: local-llm
    if_false:
      - name: escalate
        middleware: escalate
        config:
          workflow: code-escalation
          timeout: 120s
          fallback: local-llm
```

### Config Interpretation (Go)

```go
type PipelineStageConfig struct {
    Name       string                 `yaml:"name"`
    Middleware string                 `yaml:"middleware"`
    When       string                 `yaml:"when"`
    Condition  string                 `yaml:"condition"`
    IfTrue     []PipelineStageConfig  `yaml:"if_true"`
    IfFalse    []PipelineStageConfig  `yaml:"if_false"`
    Config     map[string]any         `yaml:"config"`
}

type MiddlewareFactory func(cfg map[string]any) (Middleware, error)

type Registry struct {
    factories map[string]MiddlewareFactory
}

func (r *Registry) Register(name string, f MiddlewareFactory) {
    r.factories[name] = f
}

func (r *Registry) Build(stages []PipelineStageConfig) (Middleware, error) {
    var mws []Middleware
    for _, s := range stages {
        mw, err := r.factories[s.Middleware](s.Config)
        if err != nil {
            return nil, fmt.Errorf("stage %s: %w", s.Name, err)
        }
        if s.When != "" {
            pred, err := CompileCEL(s.When)
            if err != nil {
                return nil, fmt.Errorf("stage %s CEL: %w", s.Name, err)
            }
            mw = When(pred, mw)
        }
        if s.Condition != "" {
            pred, _ := CompileCEL(s.Condition)
            trueBranch, _ := r.Build(s.IfTrue)
            falseBranch, _ := r.Build(s.IfFalse)
            mw = Branch(pred, trueBranch, falseBranch)
        }
        mws = append(mws, mw)
    }
    return Compose(mws...), nil
}
```

### CEL Predicate Compilation

CEL expressions are compiled once at config load time, evaluated in nanoseconds per request.

```go
func CompileCEL(expr string) (Predicate, error) {
    env, _ := cel.NewEnv(
        cel.Variable("msg", cel.ObjectType("jsonrpc.Message")),
        cel.Variable("sess", cel.ObjectType("Session")),
        cel.Variable("ctx", cel.MapType(cel.StringType, cel.DynType)),
    )
    ast, issues := env.Compile(expr)
    if issues.Err() != nil {
        return nil, issues.Err()
    }
    prog, _ := env.Program(ast)

    return func(sess *Session, msg *jsonrpc.Message) bool {
        out, _, _ := prog.Eval(map[string]any{
            "msg":  msg,
            "sess": sess,
            "ctx":  sess.Metadata(),
        })
        return out.Value().(bool)
    }, nil
}
```

## Middleware Examples

### Short-circuit: Security Screening

```go
func SecurityScreening(rules *Rules) Middleware {
    return func(next Handler) Handler {
        return func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error) {
            violations := rules.Check(req)

            if violations.HasCritical() {
                return jsonrpc.NewError(req.ID, CodePolicyViolation, "blocked by security policy"), nil
            }

            if violations.HasWarnings() {
                req = redact(req, violations)
            }

            resp, err := next(ctx, sess, req)
            if err != nil {
                return nil, err
            }

            if outViolations := rules.CheckResponse(resp); outViolations.Any() {
                resp = redactResponse(resp, outViolations)
            }

            return resp, err
        }
    }
}
```

### Cross-middleware data passing via context

```go
type ctxKey string

const (
    keyClassification ctxKey = "classification"
    keyViolations     ctxKey = "violations"
)

func ClassifierMiddleware() Middleware {
    return func(next Handler) Handler {
        return func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error) {
            class := classify(req)
            ctx = context.WithValue(ctx, keyClassification, class)
            return next(ctx, sess, req)
        }
    }
}

func EscalationMiddleware() Middleware {
    return func(next Handler) Handler {
        return func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error) {
            class, _ := ctx.Value(keyClassification).(Classification)
            if class.Confidence < 0.5 {
                sess.SetBackend(sess.EscalationBackend())
            }
            return next(ctx, sess, req)
        }
    }
}
```

### Async escalation via Temporal

```go
func EscalateMiddleware(temporalClient client.Client, cfg EscalateConfig) Middleware {
    return func(next Handler) Handler {
        return func(ctx context.Context, sess *Session, req *jsonrpc.Message) (*jsonrpc.Message, error) {
            run, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
                TaskQueue: "escalation",
            }, EscalationWorkflow, &EscalationInput{
                SessionID: sess.ID,
                Request:   req,
                Policy:    cfg.Policy,
            })
            if err != nil {
                return next(ctx, sess, req)
            }

            var result EscalationResult
            timeoutCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
            defer cancel()

            if err := run.Get(timeoutCtx, &result); err != nil {
                return next(ctx, sess, req)
            }

            return result.Response, nil
        }
    }
}
```

## OPA/Rego Policy Example

```rego
# policies/code-screening.rego
package screening

critical[msg] {
    input.content[i].type == "code"
    regex.match(`(?i)(password|secret|api_key)\s*[:=]`, input.content[i].text)
    msg := sprintf("hardcoded secret at position %d", [i])
}

warning[msg] {
    input.content[i].type == "code"
    contains(input.content[i].text, "internal.corp.com")
    msg := sprintf("internal URL at position %d", [i])
}
```

## Temporal Escalation Workflow

```go
func EscalationWorkflow(ctx workflow.Context, input *EscalationInput) (*EscalationResult, error) {
    var extracted CodeFragment
    err := workflow.ExecuteActivity(ctx, ExtractCodeActivity, input.Request).Get(ctx, &extracted)
    if err != nil {
        return nil, err
    }

    var screening ScreeningResult
    err = workflow.ExecuteActivity(ctx, SecurityScreenActivity, extracted, input.Policy).Get(ctx, &screening)
    if err != nil {
        return nil, err
    }

    if screening.HasCritical() {
        ch := workflow.GetSignalChannel(ctx, "approval")
        var approval bool
        ch.Receive(ctx, &approval)
        if !approval {
            return &EscalationResult{Blocked: true, Reason: "rejected by reviewer"}, nil
        }
    }

    redacted := screening.Redact(extracted)
    var externalResp ExternalResponse
    err = workflow.ExecuteActivity(ctx, CallExternalModelActivity, redacted).Get(ctx, &externalResp)
    if err != nil {
        return nil, err
    }

    var respScreening ScreeningResult
    err = workflow.ExecuteActivity(ctx, ScreenResponseActivity, externalResp).Get(ctx, &respScreening)
    if err != nil {
        return nil, err
    }

    return &EscalationResult{Response: respScreening.Clean(externalResp)}, nil
}
```

## Full Pipeline Evolution by Phase

```go
// Phase 1: transparent proxy
Pipeline(backendForwarder, LoggingMiddleware())

// Phase 2: classification + screening
Pipeline(
    backendForwarder,
    LoggingMiddleware(),
    ClassifierMiddleware(),
    When(hasCodeContent, SecurityScreening(rules)),
)

// Phase 3: smart routing + escalation
Pipeline(
    backendForwarder,
    LoggingMiddleware(),
    ClassifierMiddleware(),
    When(hasCodeContent, SecurityScreening(rules)),
    Branch(
        localModelSufficient,
        Passthrough(),
        Compose(
            CodeExtractor(),
            SecurityScreening(externalRules),
            ExternalModelForwarder(),
            ResponseScreening(responseRules),
            ResponseIntegrator(),
        ),
    ),
)
```
