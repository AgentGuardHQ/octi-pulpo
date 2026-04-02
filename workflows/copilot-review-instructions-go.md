# Copilot Code Review Instructions — Go Repos

These instructions define how Copilot should review pull requests in Go repositories.

## Review Philosophy

Approve if the code is **correct and safe**, even if style is not perfect.
Copilot PRs prioritize working software — do not block on cosmetic issues.

## Severity: Must-Fix (Request Changes)

These issues must be resolved before approval:

- **Unchecked errors** — any returned `error` that is discarded with `_` or ignored entirely.
- **Data races** — shared state accessed from multiple goroutines without synchronization (mutex, channel, or atomic).
- **Missing context propagation** — functions that do I/O or call external services without accepting `ctx context.Context`.
- **Naked returns** — `return` without values in functions with named return values, when the function body is longer than a few lines (creates confusion about what is returned).
- **Panics in library code** — `panic()` used outside of `main` or test code for recoverable errors.
- **Hardcoded secrets** — API keys, tokens, passwords, or credentials in source code.

## Severity: Should-Fix (Comment, Do Not Block)

Leave a comment but do not block approval:

- **Missing tests** — new exported functions or methods that lack corresponding test coverage.
- **Non-idiomatic patterns** — using `interface{}` instead of `any` (Go 1.18+), unnecessary `else` after early return, overly complex conditionals.
- **Large functions** — functions exceeding ~80 lines that could be broken up for readability.
- **Missing error wrapping** — errors returned without `%w` context (not a bug, but makes debugging harder).

## Severity: Nice-to-Have (Comment Only)

Optional improvements — mention if noticed, never block:

- **Godoc comments** on exported types, functions, and methods.
- **Consistent naming** of receivers across methods of the same type.
- **Package-level documentation** in `doc.go`.

## Self-Referential Protection

For the **octi-pulpo** repository specifically:

**NEVER approve changes to the `workflows/` directory.**

This directory contains the pipeline definition files (workflow YAMLs, setup scripts, and these instruction files). Changes to `workflows/` must be reviewed and approved by a human maintainer. Flag any such PR with a comment:

> This PR modifies `workflows/` which contains pipeline definitions. Human review required.

## Approval Criteria

Approve the PR if:

1. No must-fix issues found
2. Tests pass (`go test ./...`)
3. Code does what the PR description claims
4. No protected files (`.env`, `agentguard.yaml`, `.claude/`) are modified
5. Pipeline labels are not manually altered
