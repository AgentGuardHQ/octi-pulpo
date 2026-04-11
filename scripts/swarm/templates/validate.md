You are reviewing a PR for correctness, test coverage, and adherence to the original plan.

## Issue

- **Repo:** chitinhq/{{REPO}}
- **Issue:** #{{ISSUE_NUM}}
- **Title:** {{TITLE}}
- **Labels:** {{LABELS}}

## Original Plan

{{PLAN}}

## PR Diff (first 500 lines)

{{PR_DIFF}}

## Your Task

Review the PR against the plan and codebase standards:

### 1. Plan Compliance
- Does the PR implement everything in the plan?
- Does it add anything NOT in the plan?
- Are all acceptance criteria met?

### 2. Correctness
- Run `go test ./...` (or equivalent) — do all tests pass?
- Run `go build ./...` (or equivalent) — does it build?
- Are there obvious logic errors?

### 3. Quality
- Are there hardcoded values that should be constants?
- Are error messages clear?
- Is there dead code or commented-out code?

### 4. Safety
- No secrets or credentials in the diff
- No overly broad file permissions
- No SQL injection, command injection, or XSS vectors

## Verdict

After review, do ONE of:
- **Approve:** Add the `validated` label to the issue. Leave a review comment summarizing what you checked.
- **Request changes:** Add the `needs-fix` label to the issue. Leave a review comment listing specific issues to fix. Be concrete — file, line, what's wrong, how to fix.

## Constraints

- Do NOT modify any code — review only
- Do NOT merge the PR
- Be specific in feedback — "this is wrong" is not helpful, "line 42: off-by-one in loop bound, should be `< len` not `<= len`" is helpful
- If you cannot determine correctness (e.g., missing test infrastructure), add `needs-human` label and explain
