# AIBroker — Project Structure

## Directory Layout

```
aibroker/
├── cmd/
│   └── aibroker/              # main entrypoint
│       └── main.go
├── internal/
│   ├── protocol/
│   │   ├── jsonrpc/           # JSON-RPC 2.0 codec (message types, serialize/deserialize)
│   │   ├── acp/               # ACP message types, method handlers, session lifecycle
│   │   └── mcp/               # MCP message types (wrapper over mcp-go SDK)
│   ├── transport/
│   │   ├── stdio/             # stdin/stdout transport (for ACP and MCP stdio mode)
│   │   ├── sse/               # Server-Sent Events transport (MCP)
│   │   └── http/              # Streamable HTTP transport (MCP)
│   ├── proxy/
│   │   ├── pipeline.go        # Pipeline assembly, Compose, When, Branch, Router
│   │   ├── session.go         # Session manager (state, lifecycle, backend mapping)
│   │   ├── router.go          # Backend routing logic
│   │   └── registry.go        # Middleware registry (name → factory)
│   ├── middleware/
│   │   ├── middleware.go       # Handler/Middleware type definitions
│   │   ├── logging.go         # Request/response structured logging
│   │   ├── passthrough.go     # No-op passthrough (phase 1 default)
│   │   ├── screening.go       # Security screening (OPA integration point)
│   │   ├── classifier.go      # Request classification
│   │   ├── escalate.go        # Temporal escalation trigger
│   │   └── forward.go         # Backend forwarding
│   ├── config/
│   │   ├── config.go          # Top-level config struct
│   │   ├── loader.go          # YAML loading + validation
│   │   └── cel.go             # CEL expression compilation
│   └── policy/
│       ├── engine.go          # OPA engine wrapper
│       └── loader.go          # Rego policy hot-reload
├── pkg/
│   └── types/                 # Shared exported types
├── configs/
│   ├── aibroker.yaml          # Default configuration
│   └── policies/              # Rego policy files
│       └── code-screening.rego
├── docs/
│   ├── architecture.md
│   ├── middleware.md
│   └── project-structure.md
├── go.mod
└── go.sum
```

## Key Dependencies (Phase 1)

| Dependency | Purpose |
|------------|---------|
| `github.com/modelcontextprotocol/go-sdk` | Official MCP Go SDK |
| `gopkg.in/yaml.v3` | YAML config parsing |
| `log/slog` | Structured logging (stdlib) |

## Additional Dependencies (Phase 2+)

| Dependency | Purpose |
|------------|---------|
| `github.com/google/cel-go` | CEL expression evaluation |
| `github.com/open-policy-agent/opa` | OPA/Rego policy engine |
| `go.temporal.io/sdk` | Temporal workflow SDK |

## Package Responsibilities

### `cmd/aibroker`

Entrypoint. Loads config, initializes registry, builds pipeline, starts transports.

### `internal/protocol/jsonrpc`

Pure JSON-RPC 2.0 implementation: `Message`, `Request`, `Response`, `Notification`, `Error` types. Codec for marshal/unmarshal. No protocol-specific logic.

### `internal/protocol/acp`

ACP method definitions (`initialize`, `session/new`, `session/prompt`, etc.). Maps ACP messages to internal representations. Handles ACP-specific session lifecycle.

### `internal/protocol/mcp`

Thin wrapper over `mcp-go` SDK. Adapts MCP types to internal message format.

### `internal/transport/*`

Transport layer — reads/writes JSON-RPC messages over specific wire protocols. Each transport implements a common interface:

```go
type Transport interface {
    Receive(ctx context.Context) (*jsonrpc.Message, error)
    Send(ctx context.Context, msg *jsonrpc.Message) error
    Close() error
}
```

### `internal/proxy`

Core proxy logic. Pipeline assembly, session management, backend routing. Protocol-agnostic — works with internal message types.

### `internal/middleware`

All middleware implementations. Each middleware is registered in the registry by name, instantiated from YAML config via its factory.

### `internal/config`

Config loading, validation, CEL compilation. Watches config file for hot-reload.

### `internal/policy`

OPA engine integration. Loads `.rego` files, evaluates policies, returns structured results (critical/warning/info).
