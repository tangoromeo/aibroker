# AIBroker — Architecture

## Vision

Corporate service (broker/orchestrator) for coding agents with secure escalation of proprietary code to external services.

### Target Features

1. Prioritize local coding models
2. Detect when local model applicability is limited
3. Extract problematic code fragments for escalation
4. Match extracted code against security screening rules
5. Optimize interaction with external services
6. Receive and screen results from external services
7. Integrate results back into the main pipeline

## Protocol Analysis

### ACP (Agent Client Protocol)

- **Transport**: JSON-RPC 2.0 over stdin/stdout
- **Connection model**: editor boots agent as subprocess
- **Sessions**: multiple concurrent sessions per connection
- **Direction**: bidirectional — agent can request permissions from the editor
- **MCP integration**: editor passes MCP server configs to the agent; agent connects to MCP servers directly
- **Spec**: https://agentclientprotocol.com

### MCP (Model Context Protocol)

- **Transport**: JSON-RPC 2.0 over stdio / SSE / Streamable HTTP
- **Architecture**: client-host-server; each host runs multiple clients
- **Sessions**: stateful, 1:1 client-to-server with capability negotiation
- **Primitives**: resources, tools, prompts (server); sampling, roots, elicitation (client)
- **Spec**: https://modelcontextprotocol.io/specification/2025-11-25

### Kilo (reference client)

- Open-source AI coding agent for VS Code, JetBrains, CLI
- Uses MCP for tool integration (filesystem, terminal, search)
- Connects to LLM providers via HTTP API (OpenAI/Anthropic-compatible)
- Supports 500+ models, multiple agent modes
- https://kilo.ai

## Architecture Decision

### Considered Options

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A. Pipeline Proxy** | Single process, middleware pipeline, config-driven | Simple deploy, low latency, natural extension points | Single point of failure |
| **B. Sidecar + Event Bus** | Microservices over NATS/Redis Streams | Horizontal scaling, isolation | Operational complexity, latency, overkill for phase 1 |
| **C. Library/SDK** | Embeddable Go library | Zero deployment overhead, minimal latency | Coupled to agent process, harder to scale |

### Decision: Option A — Pipeline Proxy

Optimal balance for the problem domain:

- Single binary, zero external dependencies in phase 1
- Middleware pipeline maps directly to the feature roadmap
- Session manager handles multi-client concurrency
- In phase 1 the pipeline is empty (passthrough proxy)
- Features are added as middleware without changing core

## High-Level Architecture

```
                          Sync path (μs-ms)
                    ┌──────────────────────────┐
                    │    Config-driven Pipeline │
[Client] ─JSON-RPC─▶  YAML defines stages,    │──▶ [Backend]
                    │  CEL defines predicates   │
                    │         │                 │
                    └─────────┼─────────────────┘
                              │ triggers when
                              │ escalation needed
                              ▼
                    ┌──────────────────────────┐
                    │     Temporal Workflow     │    Async path (s-min)
                    │                          │
                    │  ExtractCode             │
                    │    → SecurityScreen      │──▶ [OPA/Rego Policy Engine]
                    │    → HumanApproval?      │
                    │    → SendToExternal      │
                    │    → ScreenResponse      │
                    │    → IntegrateResult     │
                    │                          │
                    └──────────────────────────┘
```

### Component Diagram

```
┌──────────────┐      ACP/stdio       ┌───────────────────────────────────────┐
│   Editor     │─────────────────────▶ │              AIBroker                 │
│ (VSCode/JB)  │ ◀───────────────────  │                                       │
└──────────────┘                       │  ┌─────────────────────────────────┐  │
                                       │  │        Protocol Front           │  │
                                       │  │  ┌─────────┐  ┌─────────────┐  │  │
                                       │  │  │ACP Inlet│  │ MCP Inlet   │  │  │
                                       │  │  │ (stdio) │  │(stdio/HTTP) │  │  │
                                       │  │  └────┬────┘  └──────┬──────┘  │  │
                                       │  └───────┼──────────────┼─────────┘  │
                                       │          │    unified   │            │
                                       │          ▼   messages   ▼            │
                                       │  ┌─────────────────────────────────┐  │
                                       │  │      Session Manager            │  │
                                       │  │  (per-client state, contexts)   │  │
                                       │  └──────────────┬──────────────────┘  │
                                       │                 │                     │
                                       │  ┌──────────────▼──────────────────┐  │
                                       │  │     Middleware Pipeline          │  │
                                       │  │  (config-driven, CEL predicates)│  │
                                       │  └──────────────┬──────────────────┘  │
                                       │                 │                     │
                                       │  ┌──────────────▼──────────────────┐  │
                                       │  │       Protocol Back             │  │
                                       │  │  ┌──────────┐  ┌────────────┐  │  │
                                       │  │  │ACP Outlet│  │ MCP Outlet │  │  │
                                       │  │  │  (stdio) │  │(HTTP/SSE)  │  │  │
                                       │  │  └──────────┘  └────────────┘  │  │
                                       │  └────────────────────────────────┘  │
                                       └───────┬───────────────────┬──────────┘
                                               │                   │
                                    ┌──────────▼───┐     ┌────────▼────────┐
                                    │ Local Agent  │     │  Internal LLM   │
                                    │   (Kilo)     │     │   Servers       │
                                    └──────────────┘     └─────────────────┘
```

### Core Components

| Component | Responsibility |
|-----------|---------------|
| **Protocol Front** | Accept inbound connections (ACP/MCP), deserialize JSON-RPC |
| **Session Manager** | Per-client state, client→session→backend mapping, lifecycle |
| **Middleware Pipeline** | Chain of handlers; each can inspect/modify/reject/reroute messages |
| **Protocol Back** | Establish and maintain connections to backends |
| **Policy Engine** | Security screening rules (OPA/Rego), hot-reloadable |
| **Temporal** | Durable async workflows for escalation (phase 2+) |

## Language Choice: Go

| Criterion | Go | Rust | TypeScript |
|-----------|-----|------|------------|
| Concurrency | goroutines + channels | tokio — powerful, steeper curve | event loop, single-threaded |
| Deployment | single binary, zero deps | single binary | requires Node.js |
| MCP SDK | mcp-go (mark3labs), official go-sdk | SDK available | SDK available |
| ACP SDK | none (implement from spec) | SDK available | SDK available |
| JSON-RPC | mature libraries | available | available |
| Memory footprint | low | lowest | high |
| Development speed | high | medium | high |

**Decision**: Go. Main trade-off — no ACP SDK, but ACP is JSON-RPC 2.0 over stdio (~15 methods), implementable in reasonable time. Full control over protocol layer is an advantage for proxy use case.

## Phased Implementation

### Phase 1: Transparent Proxy

- JSON-RPC 2.0 codec
- stdio transport
- Session manager (multi-client, concurrent sessions)
- ACP passthrough proxy (editor → broker → agent)
- MCP passthrough proxy
- YAML configuration
- Logging middleware
- No pipeline logic — pure passthrough

### Phase 2: Classification + Screening

- Request classifier middleware
- CEL-based predicates in pipeline config
- OPA/Rego policy engine integration
- Security screening middleware (block/redact)
- Code extraction middleware

### Phase 3: Smart Routing + Escalation

- Local model confidence detection
- Branch/Router middleware with config-driven conditions
- Temporal integration for async escalation workflows
- External model forwarder
- Response screening
- Result integration back into agent context

### Phase 4: Optimization

- Request deduplication
- Caching layer
- Token budget management
- Telemetry and observability
