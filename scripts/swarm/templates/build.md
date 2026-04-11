You are implementing a planned GitHub issue. Follow the plan exactly.

## Issue

- **Repo:** chitinhq/{{REPO}}
- **Issue:** #{{ISSUE_NUM}}
- **Title:** {{TITLE}}
- **Labels:** {{LABELS}}

## Description

{{BODY}}

## Implementation Plan

{{PLAN}}

## Your Task

1. Read the plan above carefully
2. Implement each step in order
3. Write or update tests as specified in the plan
4. Run `go test ./...` (or equivalent) and fix any failures
5. Run `go build ./...` (or equivalent) and fix any build errors
6. Commit your changes with a clear commit message referencing the issue: `fix|feat|test|chore(scope): description (#{{ISSUE_NUM}})`
7. Push the branch and create a PR targeting the default branch

## Constraints

- Follow the plan — do not add features or refactors not in the plan
- All tests must pass before creating the PR
- The build must succeed before creating the PR
- Do not modify files not listed in the plan unless fixing a test or build error
- If the plan is wrong or incomplete, add a comment to the issue explaining the problem and add the `needs-human` label. Do NOT guess.
- Keep commits atomic — one logical change per commit
- PR title format: `type(scope): description (#{{ISSUE_NUM}})`
