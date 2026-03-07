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
- **Tenancy mode**: `single` today; `multi` is reserved as TODO.

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

- `cmd/elok`: single CLI entrypoint (`run`, `version`)
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

## Web UI (Embedded PWA)

The chat UI is a SvelteKit PWA embedded directly into the `elok` binary.

- Source: `ui/src/*`
- Embedded assets: `ui/dist/*` via `ui/embed.go`
- Served by gateway at `/app` with SPA fallback to `index.html`

Build UI assets before running or committing UI changes:

```bash
cd ui
npm install
npm run build
```

Then run elok:

```bash
go run ./cmd/elok run
```

Open:

- `http://127.0.0.1:7777/app` for chat UI
- `ws://127.0.0.1:7777/ws` for gateway RPC
- `http://127.0.0.1:7777/` redirects to `/app`

## Dev Loop (Air)

Run the hot-reload dev loop:

```bash
make ui-install
make dev
```

Behavior:

- Go changes: rebuild `./tmp/elok` and restart.
- UI changes under `ui/` (excluding `ui/dist`, `.svelte-kit`, `node_modules`): run `make ui`, then rebuild/restart Go binary.
- No UI changes: skip UI build for faster loop.

## Terminal Bench with Harbor

You can benchmark the `elok` gateway loop (`session.send`) against Harbor datasets, including Terminal Bench.

This repo includes:

- `bench/harbor/elok_agent.py`: Harbor custom agent that forwards task instructions to `ws://.../ws` (`session.send`).
- `scripts/run-harbor-terminal-bench.sh`: helper script to run Harbor with the custom agent.

### 1) Start elok

In one terminal:

```bash
go run ./cmd/elok run
```

### 2) Run a quick smoke benchmark

In another terminal (from repo root):

```bash
./scripts/run-harbor-terminal-bench.sh --n-tasks 3 --n-concurrent 1 --disable-verification
```

Notes:

- The script uses `uvx` automatically when available (`uvx --python 3.12 --with harbor --with websockets ...`).
- If `uvx` is not installed, it falls back to `harbor` from your PATH.

### 3) Run larger Terminal Bench datasets

Sample set (default):

```bash
ELOK_HARBOR_DATASET=terminal-bench-sample@2.0 ./scripts/run-harbor-terminal-bench.sh
```

Full set:

```bash
ELOK_HARBOR_DATASET=terminal-bench@2.0 ./scripts/run-harbor-terminal-bench.sh --n-concurrent 8
```

Pro set:

```bash
ELOK_HARBOR_DATASET=terminal-bench-pro@1.0 ./scripts/run-harbor-terminal-bench.sh --n-concurrent 8
```

### 4) Configure runtime overrides (optional)

Environment variables:

- `ELOK_GATEWAY_URL` (default `ws://127.0.0.1:7777/ws`)
- `ELOK_TENANT_ID` (default `default`)
- `ELOK_HARBOR_DATASET` (default `terminal-bench-sample@2.0`)
- `ELOK_HARBOR_JOBS_DIR` (default `jobs/harbor`)
- `ELOK_SEND_PROVIDER` (sets `session.send.provider`)
- `ELOK_SEND_MODEL` (sets `session.send.model`)

### 5) Inspect results

Job artifacts are written to `jobs/harbor` by default. Use Harbor's viewer:

```bash
harbor view -p jobs/harbor/<job_id>
```
