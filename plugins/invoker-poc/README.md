# invoker-poc plugin

Fast proof-of-concept plugin that pairs `elok` with Invoker and starts a `cloudflared` tunnel to the local `elok` gateway.

This plugin is intentionally minimal:
- Command-driven via `/cstunnel`
- QuickJS hot-reload command parser
- OAuth + machine register + tunnel lifecycle in Go wrapper

## Run command

Use this as a plugin entry:

```toml
[[plugins.entries]]
id = "invoker-poc"
command = ["go", "run", "./plugins/invoker-poc/cmd/invokerpoc"]
```

You can keep `plan-mode` enabled and add this as an extra plugin.

## Commands

- `/cstunnel` (main command; runs pair/login/register/tunnel flow)
- `/cstunnel status`
- `/cstunnel stop`
- `/cstunnel help`

`/cstunnel` is iterative:
- first call returns auth URL
- after browser login, call again and it continues automatically

## Optional env vars

- `ELOK_INVOKER_BASE_URL` (default `https://counterspell.io`)
- `ELOK_INVOKER_PROVIDER` (default `google`)
- `ELOK_INVOKER_REDIRECT_URI` (default `https://counterspell.io/api/v1/auth/callback`)
- `ELOK_INVOKER_LOCAL_URL` (default `http://127.0.0.1:7777`)
- `ELOK_INVOKER_ZONE` (default `counterspell.app`)
- `ELOK_INVOKER_CLOUDFLARED_PATH` (optional, if `cloudflared` not on `PATH`)
- `ELOK_INVOKER_POC_STATE` (default `~/.elok/invoker-poc.json`)
- `ELOK_INVOKER_POC_SCRIPT` (override JS path; hot-reloaded)
