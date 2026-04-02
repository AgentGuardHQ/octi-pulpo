# Copilot Coding Instructions — Go Repos

These instructions apply to all Copilot-authored code in this repository.

## Code Style

- All code must pass `gofmt`. Do not submit unformatted code.
- All code must pass `go vet` with zero findings.
- Follow standard Go conventions: short variable names in tight scopes, exported names documented, receiver names consistent.

## Error Handling

- Always check returned errors. Never discard an error with `_`.
- Wrap errors with context using `fmt.Errorf("doing X: %w", err)` so callers can unwrap.
- Return errors rather than logging and continuing — let the caller decide.

## Context Propagation

- Always accept `ctx context.Context` as the first parameter in functions that do I/O, call external services, or may block.
- Pass `ctx` through the entire call chain. Never create a detached `context.Background()` mid-chain unless intentionally decoupling lifecycle.
- Respect context cancellation — check `ctx.Err()` in long-running loops.

## Testing

- Use table-driven tests for any function with more than one meaningful input case.
- Name test cases clearly: `{name: "empty input returns error", ...}`.
- Use `t.Helper()` in test helper functions.
- Run `go test ./...` before marking any PR as ready for review.

## Module Layout

- Keep `cmd/` for entrypoints, `internal/` for unexported packages, `pkg/` for exported libraries.
- One package per directory. Package name matches directory name.
- Avoid circular imports — if two packages need each other, extract a shared interface.

## PR Requirements

- Prefix PR titles with a type: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`
- Keep PRs under 500 lines changed.
- Keep PRs under 20 files changed.
- Each PR should address a single concern — do not bundle unrelated changes.

## Protected Files — Do Not Modify

The following paths are off-limits. Do not create, edit, or delete them:

- `.env` and any `.env.*` files
- `agentguard.yaml`
- `.claude/` directory and all contents

## Pipeline Labels

Pipeline labels are managed by the Octi Pulpo pipeline and are **read-only**.
Do not add, remove, or modify these labels manually:

- `tier:c`, `tier:b-scope`, `tier:b-code`, `tier:a`, `tier:a-groom`
- `tier:ci-running`, `tier:review`, `tier:needs-revision`
- `triage:needed`, `needs:human`, `agent:review`

## Pre-Submit Checklist

Before marking a PR as ready:

1. `gofmt` — code is formatted
2. `go vet ./...` — no static analysis issues
3. `go test ./...` — all tests pass
4. PR title has a type prefix
5. No protected files modified
