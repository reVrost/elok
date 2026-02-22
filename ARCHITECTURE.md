# Elok Architecture

This document explains the current architecture of `elok` as implemented in this repo, with emphasis on the agentic loop and extension model.

## 1. Executive Summary

`elok` is a single-process Go host that exposes an agent over a WebSocket gateway, persists chat history in SQLite, and extends behavior through out-of-process plugins over newline-delimited JSON-RPC on stdio.

Today, the implemented runtime loop is:

1. Receive request via gateway (`session.send`).
2. Persist user message.
3. Let plugins optionally intercept slash commands.
4. Let plugins mutate prompt/user text via hooks.
5. Call LLM provider adapter.
6. Persist assistant response.
7. Notify post-turn plugin hooks.

This is a pragmatic "agent host scaffold" rather than a full autonomous planner/executor: tool calling is registered in-process but not yet wired into the LLM turn loop.

## 2. System Boundaries

### In process

- API/gateway server (`pkg/gateway`)
- Agent service (`pkg/agent`)
- LLM adapter interface + providers (`pkg/llm`)
- Tool registry (`pkg/tools`)
- Store/migrations (`pkg/store`)
- Plugin process orchestrator (`pkg/plugins`)

### Out of process

- Plugin binaries (example: `plugins/plan-mode/cmd/planmode`)
- External LLM endpoint for `openrouter`

### Conditionally wired channels

- WhatsApp adapter (`pkg/channels/whatsapp/adapter.go`) is wired when `whatsapp.enabled=true`.

## 3. Repository Map

- `cmd/elok/main.go`: CLI entrypoint (`run`, `migrate`, `init-config`, `version`).
- `pkg/config/config.go`: config schema, defaults, load/save, path expansion.
- `pkg/agent/service.go`: core turn processing and session APIs.
- `pkg/gateway/server.go`: WS/HTTP transport, method dispatch.
- `pkg/gateway/schema.go`: envelope and RPC payload structs.
- `pkg/llm/*.go`: provider factory + mock/openrouter/codex adapters.
- `pkg/tools/registry.go`: in-memory tool definitions + execution.
- `pkg/plugins/manager.go`: plugin process lifecycle + RPC client.
- `pkg/plugins/protocol/types.go`: plugin RPC contract types.
- `pkg/store/store.go`: SQLite access layer + embedded SQL migrations.
- `pkg/store/migrations/*.sql`: schema DDL.
- `plugins/plan-mode/cmd/planmode/main.go`: reference plugin.
- `schemas/*.json`: JSON schema + method map for gateway envelope.

## 4. Startup and Runtime Lifecycle

`elok run` does the following in `cmd/elok/main.go`:

1. Loads config from `~/.elok/config.toml` (or `-config`).
2. Installs default text logger (`slog`).
3. Opens SQLite store and applies embedded migrations.
4. Starts plugin manager, which starts each configured plugin process and calls `register`.
5. Constructs `agent.Service` with store + llm client + plugin manager + tool registry.
6. Starts gateway HTTP server (`/healthz`, `/ws`).
7. On signal (`SIGINT`/`SIGTERM`), shuts down gateway and plugin processes.

Other commands:

- `elok migrate`: load config and apply migrations.
- `elok init-config`: write default config TOML.
- `elok version`: print build version string.

## 5. Agentic Loop (Current Implementation)

Primary entrypoint: `agent.Service.Send(ctx, sessionID, text)`.

### Turn algorithm

1. Validate input text non-empty.
2. Generate session ID (`s_<16 hex>`) if caller omitted one.
3. Persist user message first (`role=user`).
4. Call `plugins.HandleCommand`.
5. If command handled:
6. Persist plugin response as assistant message.
7. Return without LLM call.
8. Else call `plugins.BeforeTurn` to allow:
9. User text mutation.
10. System prompt append.
11. Load up to 40 prior messages from store.
12. Build transcript from stored `user`/`assistant` messages.
13. If hook-mutated text differs, append mutated text as an additional user message in transcript (not persisted).
14. Call `llm.Client.Complete` with system prompt + transcript.
15. Persist assistant response (or `"(empty assistant response)"` fallback).
16. Call `plugins.AfterTurn` best-effort (errors logged, not returned).
17. Return session id + assistant text.

### Design implications

- Durability-first: user input is persisted before any plugin/model logic.
- Command path short-circuits model invocation.
- Hook mutations are ephemeral unless the plugin separately persists state.
- No tool-call roundtrip loop yet (no parse/execute/retry cycle in `agent.Service`).

### Effective loop maturity

Implemented: single-shot completion with plugin interceptors and provider-level token streaming support.
Not implemented yet: iterative plan/act/observe loops, tool result chaining, guardrails, approval gates, gateway-level streaming to clients.

## 6. Plugin Runtime Architecture

Plugin system is out-of-process and host-driven.

### Process model

- Host starts plugin command from config (`exec.CommandContext`).
- Host writes JSON envelopes to plugin stdin.
- Host reads line-delimited JSON envelopes from plugin stdout.
- Plugin stderr is logged through host logger.

### RPC envelope

Shared contract (`pkg/plugins/protocol/types.go`):

- `call`: request with `id`, `method`, `params`.
- `result`: successful response with `result`.
- `error`: failure with `{code,message}`.
- `event`: asynchronous plugin-originated event.

### Method lifecycle

Required:

- `register` -> plugin metadata/capabilities.

Optional capabilities:

- `command.handle` (slash-command interception)
- `hook.before_turn`
- `hook.after_turn`
- `tools` capability flag exists in protocol but host does not execute plugin tools yet.

### Call mechanics

- Host allocates monotonic request IDs per plugin.
- Pending response channels are tracked in a map keyed by request ID.
- Context cancel/timeouts remove pending entry and fail call.
- If stdout closes, host fails all pending calls with `plugin_closed`.

### Ordering semantics

- Commands: first plugin returning `handled=true` wins.
- Hooks: evaluated in configured plugin order.
- `before_turn`: each plugin receives prior plugin's mutated user text.
- `after_turn`: all hook-capable plugins called sequentially; failures do not fail user turn.

### Reference plugin (`plan-mode`)

`plugins/plan-mode` demonstrates:

- Session-scoped plan/execution state handled by a script runtime (`/plan on|off|status|execute`, `/todos`).
- `hook.before_turn` context injection for plan mode and execution mode.
- `hook.after_turn` that extracts numbered plan steps and tracks `[DONE:n]` completion markers.
- Script hot-reload on file changes (`plugins/plan-mode/cmd/planmode/runtime/plan_mode.js`).

This plugin is effectively prompt shaping + command UX, not a planner/executor engine.

## 7. Gateway and Client Protocol

Transport: WebSocket endpoint `/ws` plus HTTP `/healthz`.

### Envelope contract

Envelope fields are in `pkg/gateway/schema.go` and mirrored by `schemas/gateway-envelope.schema.json`.

Supported methods:

- `system.ping`
- `session.send`
- `session.list`
- `session.messages`

### Method behavior

- `session.send`: calls agent loop and returns `{session_id, assistant_text, handled_command}`.
- `session.list`: returns recent sessions by `last_message_at DESC`.
- `session.messages`: returns messages for a session, oldest-first in final payload.

### Error behavior

Gateway wraps failures as envelope errors with code/message strings (for example `bad_params`, `send_failed`, `method_not_found`).

### Security note

`websocket.Accept` currently sets `InsecureSkipVerify: true` and there is no authentication/authorization layer. Treat current gateway as local-trust/dev posture.

## 8. Persistence Model

Database: SQLite via `modernc.org/sqlite` in host store package.

Tables:

- `sessions`: id + timestamps (`created_at`, `updated_at`, `last_message_at`).
- `messages`: auto-increment id, session FK, role/content, created_at.

Indexes:

- `idx_messages_session_id(session_id, id)`
- `idx_sessions_last_message_at(last_message_at DESC)`

Store API highlights:

- `AppendMessage` upserts session then inserts message then updates session timestamps.
- `ListMessages` reads newest-first from SQL then reverses in memory for chronological output.
- Migrations are embedded and executed on each startup (`CREATE IF NOT EXISTS` style, no migration state table).

## 9. LLM Provider Layer

Factory: `llm.New(cfg)` chooses by `llm.provider`:

- `mock`: deterministic echo-like behavior for local dev.
- `openrouter`: calls `/chat/completions` and supports SSE token streaming.
- `codex`: supports OpenAI Responses API with API key mode or ChatGPT/Codex OAuth auth.json mode.

Request shape passed from agent:

- `SystemPrompt` string
- `Messages[]` transcript (role/content)

Provider adapters expose a streaming interface; the current agent loop still uses one-shot completion semantics.

## 10. Tooling Layer (Current State)

`pkg/tools/registry.go` provides:

- Tool registration with JSON-like input schema metadata.
- Runtime execution by name.
- Built-in `time.now` tool.

Current gap:

- Registry is created and injected into `agent.Service`, but never used by turn loop or plugins.
- Plugin protocol advertises `tools` capability but host never routes tool invocations.

So tool support is scaffolded API surface, not active behavior.

## 11. WhatsApp Channel Status

`pkg/channels/whatsapp/adapter.go` includes substantial integration scaffolding:

- Persistent whatsmeow sql store.
- QR-based device linking when no device ID exists.
- Incoming text extraction (conversation/extended text/captions).
- `OnText` callback and outbound `SendText`.

Current runtime status:

- Main program (`cmd/elok`) instantiates this adapter when `whatsapp.enabled=true`.
- Incoming WhatsApp text is bridged into `agent.Service.Send` with session IDs keyed as `wa:<chatID>`.
- Assistant text responses are sent back to WhatsApp via adapter `SendText`.

Interpretation: WhatsApp is a first-class ingress/egress channel in the host runtime, gated by config.

## 12. Config Surface

Config struct (`pkg/config/config.go`) includes:

- `db_path`
- `listen_addr`
- `logging` (`format`, `level`)
- `observability` (`victoria_logs_url`, batching/queue/timeout controls)
- `llm` (`provider`, `model`, `api_key_env`, `base_url`, `codex_auth_path`)
- `plugins` (`enabled`, plugin entries with command argv)
- `gateway` (`enable_ws`)
- `whatsapp` (`enabled`, `store_path`)

Defaults are opinionated for local development:

- Local db under `~/.elok/elok.db`
- Gateway on `127.0.0.1:7777`
- Mock LLM
- `plan-mode` plugin launched via `go run`

Note: `gateway.enable_ws` is present in config but gateway startup does not currently gate endpoint registration on it.

## 13. Concurrency and Failure Semantics

### Concurrency

- HTTP server handles connections concurrently.
- Each WS connection loop processes requests sequentially as read/write operations.
- Plugin manager supports concurrent in-flight RPCs per plugin via pending map and request IDs.
- `plan-mode` plugin maintains in-memory session JSON state guarded by RW mutex and serializes QuickJS calls with a runtime mutex.

### Failure and recovery

- Plugin startup failure aborts host startup.
- Plugin call failure in command/before_turn fails user request.
- Plugin call failure in after_turn is logged and ignored.
- LLM failure surfaces to caller as `send_failed`.
- No retry/backoff or circuit breaker logic yet.

## 14. Observability and Operability

Current observability is log-centric:

- Startup/shutdown and plugin load logs via `slog`.
- Plugin stderr is surfaced in host logs.
- Logging supports text/json output and optional direct export to VictoriaLogs via `/insert/jsonline`.
- Log events include stable dimensions such as `source`, `component`, and request/session identifiers where available.
- Gateway has only `healthz` and no metrics endpoint.
- No structured event stream for turns/tools yet.

Minimal operational checks:

1. `elok init-config`
2. `elok run`
3. `curl http://127.0.0.1:7777/healthz`
4. Connect WS client and call `system.ping` / `session.send`.

## 15. Build and Dependency Notes (Current Scaffold)

Current scaffold uses:

- `go.mau.fi/whatsmeow` for WhatsApp.
- `github.com/ncruces/go-sqlite3/driver` for a no-CGO sqlite driver in the WhatsApp store path.

At time of writing, `go test ./...` passes in this repo.

## 16. Implemented vs Intended Architecture

### Implemented now

- Single binary host lifecycle.
- SQLite-backed sessions/messages.
- WebSocket request/response gateway.
- LLM abstraction with mock/openrouter providers.
- Out-of-process plugin runtime with command and hook extensions.
- Optional first-class WhatsApp channel bridge enabled from config.

### Intended but not yet complete

- End-to-end WhatsApp channel integration in main runtime.
- Active tool-calling loop inside agent turns.
- Plugin-provided tools execution.
- Approval/sandbox policy layer.
- Rich event streaming + inspection UI.

## 17. How to Evolve into a Strong Agentic Runtime

If your goal is a production-grade agentic loop, the next architectural increments are:

1. Introduce explicit turn state machine.
2. Add model output parsing for tool intents and a bounded loop (`max_steps`) around think -> act -> observe.
3. Route tool execution through `tools.Registry`, then append tool results back into transcript.
4. Add capability-aware plugin tool bridge (`tools` in protocol) with timeouts and resource quotas.
5. Add per-turn trace IDs and structured event emission for observability.
6. Split command plane vs conversation plane to avoid slash-command pollution in history.
7. Gate external side effects behind policy checks before tool/plugin execution.

A practical target loop:

1. Build context.
2. Ask model for next action.
3. If final answer, persist/return.
4. If tool call, validate + execute.
5. Persist tool result.
6. Iterate until stop condition.

## 18. Mental Model for This Repo

Use this framing while reading/changing code:

- `pkg/agent` is the orchestrator, not the intelligence.
- `pkg/llm` is swappable model transport.
- `pkg/plugins` is extension and policy surface.
- `pkg/store` is the source of conversational truth.
- `pkg/gateway` is protocol boundary for clients.
- `pkg/channels/*` are ingress/egress adapters.

When these are cleanly separated, you can independently evolve model strategy, transport, and channels without rewriting core orchestration.
