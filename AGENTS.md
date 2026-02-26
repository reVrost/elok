# AGENTS

- If changing Go code, run `go test ./...` before finishing.
- Do not hand-edit `ui/dist/*`; rebuild from `ui/` sources when needed.

## Engineering Principles
- Use `context.Context` first in method signatures and always propagate it.
- Wrap errors: `fmt.Errorf("context: %w", err)`.
- Use structured logging (`slog.Info("msg", "key", val)`).
- Put utility/shared helper functions in `pkg/shared`.
- Prefer `sqlc` queries over ad-hoc raw SQL in Go code.
- Bespoke/raw SQL in Go code is banned for new changes; use `sqlc` query files + generated code.
- Keep changes scoped; avoid unrelated refactors.

## API Change Checklist
- Decide whether a change is breaking; clarify redirects/back-compat if unclear.
- Update server routes and all clients consistently.
- Call out documentation updates when behavior or APIs change.

## Testing Expectations
- If tests are skipped, state it explicitly in the final response.

## UI Guidelines
- UI is Svelte 5: prefer runes (`$state`, `$effect`, `$derived`) over legacy reactivity patterns.
- Empty states should be short, neutral, and actionable.
