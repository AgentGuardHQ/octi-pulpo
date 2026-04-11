#!/usr/bin/env bash
# post-dispatch.sh — deterministic result validation after agent completes.
# Called by Octi dispatch loop. Exits non-zero if agent output is invalid.
# Usage: post-dispatch.sh <platform> <repo> <issue_number> <queue> <worktree_dir> <exit_code>
set -euo pipefail

PLATFORM="${1:?platform required}"
REPO="${2:?repo required}"
ISSUE_NUM="${3:?issue number required}"
QUEUE="${4:?queue required}"
WORKTREE_DIR="${5:?worktree dir required}"
EXIT_CODE="${6:?exit code required}"

WORKSPACE="${OCTI_WORKSPACE:-$HOME/workspace}"
REPO_DIR="$WORKSPACE/$REPO"

warn() { echo "POST-DISPATCH WARN: $*" >&2; }
fail() { echo "POST-DISPATCH FAIL: $*" >&2; RESULT="failed"; }

RESULT="success"

# 1. Check agent exit code
if [[ "$EXIT_CODE" -ne 0 ]]; then
  fail "agent exited with code $EXIT_CODE"
fi

# 2. Queue-specific validation
case "$QUEUE" in
  intake)
    # Plan queue: agent should have produced a plan comment, no code changes expected
    # Check if a comment was added to the issue
    COMMENT_COUNT=$(gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM/comments" --jq 'length' 2>/dev/null || echo "0")
    [[ "$COMMENT_COUNT" -gt 0 ]] || warn "no plan comment found on issue #$ISSUE_NUM"
    ;;

  build)
    # Build queue: agent should have commits in the worktree
    if [[ -d "$WORKTREE_DIR" ]]; then
      COMMITS=$(git -C "$WORKTREE_DIR" log --oneline HEAD...HEAD~10 2>/dev/null | wc -l || echo "0")
      [[ "$COMMITS" -gt 0 ]] || fail "no commits in worktree — agent produced no code"

      # Tests must pass
      if [[ -f "$WORKTREE_DIR/go.mod" ]]; then
        if ! (cd "$WORKTREE_DIR" && go test ./... -count=1 -timeout 120s >/dev/null 2>&1); then
          fail "go tests fail in worktree"
        fi
      elif [[ -f "$WORKTREE_DIR/package.json" ]]; then
        if ! (cd "$WORKTREE_DIR" && npm test >/dev/null 2>&1); then
          fail "npm tests fail in worktree"
        fi
      fi

      # Build must succeed
      if [[ -f "$WORKTREE_DIR/go.mod" ]]; then
        if ! (cd "$WORKTREE_DIR" && go build ./... >/dev/null 2>&1); then
          fail "go build fails in worktree"
        fi
      fi

      # Check for obvious problems: large binary files, secrets
      LARGE_FILES=$(git -C "$WORKTREE_DIR" diff --cached --name-only --diff-filter=A 2>/dev/null | while read f; do
        SIZE=$(wc -c < "$WORKTREE_DIR/$f" 2>/dev/null || echo 0)
        [[ "$SIZE" -gt 1048576 ]] && echo "$f ($SIZE bytes)"
      done || true)
      [[ -z "$LARGE_FILES" ]] || warn "large files added: $LARGE_FILES"

      SECRET_PATTERNS='PRIVATE_KEY|SECRET|PASSWORD|API_KEY|TOKEN.*=.*[a-zA-Z0-9]{20}'
      SECRETS=$(git -C "$WORKTREE_DIR" diff HEAD~1..HEAD 2>/dev/null | grep -iE "$SECRET_PATTERNS" | head -3 || true)
      [[ -z "$SECRETS" ]] || fail "possible secrets in diff: $SECRETS"
    else
      fail "worktree $WORKTREE_DIR does not exist"
    fi
    ;;

  validate)
    # Validate queue: agent should have reviewed and left a verdict
    # Check for review comment on the associated PR
    PR_NUM=$(gh api "repos/chitinhq/$REPO/pulls?state=open&head=chitinhq:swarm/build-$ISSUE_NUM" --jq '.[0].number' 2>/dev/null || echo "")
    if [[ -n "$PR_NUM" ]]; then
      REVIEW_COUNT=$(gh api "repos/chitinhq/$REPO/pulls/$PR_NUM/reviews" --jq 'length' 2>/dev/null || echo "0")
      [[ "$REVIEW_COUNT" -gt 0 ]] || warn "no review found on PR #$PR_NUM"
    else
      warn "no PR found for issue #$ISSUE_NUM"
    fi
    ;;
esac

# 3. Output result as JSON for Octi to consume
cat <<EOF
{
  "result": "$RESULT",
  "platform": "$PLATFORM",
  "repo": "$REPO",
  "issue": $ISSUE_NUM,
  "queue": "$QUEUE",
  "exit_code": $EXIT_CODE
}
EOF

[[ "$RESULT" == "success" ]] && exit 0 || exit 1
