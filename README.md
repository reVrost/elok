# elok

**elok** is a hackable, extensible agent host built in **Go**, designed around a small core plus runtime-loadable extensions (tools + commands + lifecycle hooks).

## Why elok?

- **Go core**: fast, easy concurrency, clean deployment.
- **Hackable extensions**: plugins can add behavior without changing core.
- **Primitives, not features**: keep core small; ship workflows as agent/profile/plugin composition.

## Locked decisions

- **Single binary**: one executable `elok` (subcommands), no `elokd`/`elokctl` split.
- **WhatsApp is first-class**: built into core channels layer using `whatsmeow` (personal account flow).
- **No default gates**: unleashed by default (no approval/sandbox UX in v0).
- **No CGO requirement for core architecture**.
- **First plugin target**: `plan-mode` plugin (WhatsApp is no longer the first plugin target).
- **Global state location**: all runtime/config state under `~/.elok`.
- **Config format**: TOML at `~/.elok/config.toml`.

## What “extensible” means here

A plugin can:
- **register tools** (callable by the model)
- **register commands** (e.g. `/help`, `/tools`, `/reset`)
- **subscribe to lifecycle events** (modify tool calls, inject context, log, enforce custom policies)
- **persist state** (per user/session)

This mirrors Pi/OpenClaw style extension points: register tools/commands, hook lifecycle, and hot-reload safely.

## MVP goal (v0)

1) Run `elok run` locally as a long-running host process.
2) Connect WhatsApp personal account via `whatsmeow`.
3) Route inbound messages into SQLite-backed sessions (per chat/sender).
4) Produce agent replies through the same WhatsApp account.
5) Load one runtime plugin: `plan-mode` (command + lifecycle behavior).
6) Provide a minimal UI for inspecting sessions, stream events, and channel status.

## Architecture (high level)

### Core packages

- `cmd/elok`: single CLI entrypoint (`run`, `migrate`, `dev`, etc.)
- `pkg/agent`: agent loop (messages -> model -> tool calls -> results) + event stream
- `pkg/llm`: provider adapters (`openrouter`, `codex`)
- `pkg/tools`: tool registry + JSON schema validation + execution
- `pkg/plugins`: runtime plugin manager (load/reload/unload)
- `pkg/channels`: channel manager/runtime lifecycle
- `pkg/channels/whatsapp`: first-class WhatsApp integration via `whatsmeow`
- `pkg/gateway`: client transport layer (UI/CLI integration)
- `pkg/store`: SQLite schema + sqlc query layer

### Gateway status surface

- WS method: `system.channels` -> current channel runtime status.
- HTTP: `GET /status/channels` -> same channel status snapshot.

### Agent and plugin packaging

- `agents/default`: baseline agent profile used for MVP testing.
- `plugins/plan-mode`: first plugin that modifies behavior and exposes plan controls.

### Plugin architecture (v0 minimal)

- **Plugin authoring language**: Go (first-class, official SDK).
- **Execution model**: out-of-process plugin binaries managed by host (`pkg/plugins`).
- **IPC protocol**: newline-delimited JSON-RPC over stdio (minimal envelope, versioned).
- **Why this choice**:
  - avoids Go `plugin` runtime portability issues.
  - no CGO requirement in core architecture.
  - runtime load/reload is simple (`start`, `stop`, `restart` plugin process).
  - keeps door open for non-Go plugins later via same protocol.

#### Initial plugin surface

- `register`: plugin declares `id`, `version`, `capabilities`.
- `hooks`: optional lifecycle callbacks (before/after turn/tool call).
- `commands`: optional slash/host commands.
- `tools`: optional tool handlers callable by agent.

#### Deferred (not v0)

- General embedded multi-runtime plugin SDK in core (Lua/goja/QuickJS beyond targeted plugins).
- Complex plugin marketplace/distribution flow.

### Persistence

- SQLite with `sqlc`.
- Store sessions, messages, events, plugin state, and channel/account mapping.

### LLM provider notes

- LLM adapters expose `Complete` and `Stream` interfaces in `pkg/llm`.
- `llm.provider = "openrouter"` uses `llm.api_key_env` against `/chat/completions` streaming.
- `llm.provider = "codex"` supports either:
  - ChatGPT/Codex subscription auth via `llm.codex_auth_path` (default `~/.codex/auth.json`).
  - OpenAI API key mode via `llm.api_key_env` (for example `OPENAI_API_KEY`).

## Local Observability (VictoriaLogs)

`elok` can export structured JSON logs directly to VictoriaLogs.

1. Start VictoriaLogs:

```bash
docker compose up -d
```

2. Enable JSON logging + export in `~/.elok/config.toml`:

```toml
[logging]
format = "json"
level = "info"

[observability]
victoria_logs_url = "http://127.0.0.1:9428/insert/jsonline"
victoria_logs_queue_size = 1024
victoria_logs_flush_ms = 500
victoria_logs_batch_size = 262144
victoria_logs_timeout_ms = 3000
```

3. Run `elok`:

```bash
go run ./cmd/elok run
```

4. Inspect logs in VictoriaMetrics UI:

- UI: `http://127.0.0.1:9428/select/vmui/`
- Example query: `service:elok`
- Useful fields for filtering:
  - `source` (currently `core`)
  - `component` (`gateway`, `agent`, `plugins`, `channels`)
  - `request_id` (gateway calls)
  - `session_id` (agent/session.send path)
