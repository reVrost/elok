# Plugin Runtime (v0)

`elok` uses out-of-process plugins over newline-delimited JSON-RPC via stdio.

## Envelope

```json
{"type":"call","id":"1","method":"register","params":{}}
{"type":"result","id":"1","result":{}}
{"type":"error","id":"1","error":{"code":"bad_params","message":"..."}}
{"type":"event","event":"plugin.log","data":{}}
```

## Required method

- `register` -> `protocol.RegisterResult`

## Optional methods

- `command.handle` -> `protocol.CommandHandleResult`
- `hook.before_turn` -> `protocol.HookBeforeTurnResult`
- `hook.after_turn` -> ack object

See `plugins/plan-mode/cmd/planmode/main.go` for a complete example plugin.

The `plan-mode` example evaluates JS logic through QuickJS and hot-reloads
`plugins/plan-mode/cmd/planmode/runtime/plan_mode.js` when the file changes.
Override the script path with `ELOK_PLANMODE_SCRIPT=/absolute/path/to/plan_mode.js`.
