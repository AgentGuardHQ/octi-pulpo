#!/usr/bin/env bash
# feed-openclaw.sh — proactive maintenance dispatcher for OpenClaw.
# Sends synthetic groom/audit tasks via Matrix when openclaw is idle.
# No issue number required — work is self-generated.
#
# Usage: feed-openclaw.sh [task-type]
#   task-type: groom | audit-labels | stale-check | doc-lint (default: rotate)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Load env file if present (cron uses /bin/sh which doesn't propagate
# sourced vars the same way bash does, so we source inside the script).
ENV_FILE="${OCTI_OPENCLAW_ENV:-/home/jared/workspace/octi/server/openclaw-feed.env}"
if [[ -r "$ENV_FILE" ]]; then
  set -a
  # shellcheck source=/dev/null
  . "$ENV_FILE"
  set +a
fi

WORKSPACE="${OCTI_WORKSPACE:-$HOME/workspace}"
LOG_DIR="${OCTI_LOG_DIR:-$HOME/.local/share/octi/swarm}"
STATE_FILE="$LOG_DIR/openclaw-feed-state.json"

HOMESERVER="${MATRIX_HOMESERVER:-http://localhost:8008}"
TOKEN="${OCTI_MATRIX_TOKEN:-}"
ROOM_ID="${OPENCLAW_ROOM_ID:-}"
BOT_USER="${OPENCLAW_BOT_USER_ID:-@openclaw-bot:localhost}"

mkdir -p "$LOG_DIR"
log() { echo "[$(date -u +%H:%M:%S)] feed-openclaw: $*"; }

# ── Flow emit helpers (chitin flow emit — no-op if chitin missing) ───
# Pairs with sentinel analyzers (e.g. sentinel#47 unacked-dispatch) so a
# dispatched-but-never-acked task is visible instead of a black hole.
flow_emit() {
  local name="$1" status="$2"; shift 2
  command -v chitin >/dev/null 2>&1 || return 0
  chitin flow emit "$name" "$status" "$@" 2>/dev/null || true
}
flow_start()    { flow_emit "$1" started "${@:2}"; }
flow_complete() { flow_emit "$1" completed "${@:2}"; }
flow_fail()     { flow_emit "$1" failed --reason "$2" "${@:3}"; }

# Parent span — covers the whole run, closed via EXIT trap.
PARENT_FLOW="swarm.feed_openclaw"
PARENT_STATUS="completed"
PARENT_REASON=""
flow_start "$PARENT_FLOW"
_feed_exit() {
  local rc=$?
  if [[ "$rc" -ne 0 && "$PARENT_STATUS" == "completed" ]]; then
    PARENT_STATUS="failed"
    PARENT_REASON="exit_rc${rc}"
  fi
  if [[ "$PARENT_STATUS" == "failed" ]]; then
    flow_fail "$PARENT_FLOW" "${PARENT_REASON:-unknown}"
  else
    flow_complete "$PARENT_FLOW"
  fi
}
trap _feed_exit EXIT

# ── Preflight ────────────────────────────────────────────────────────
if [[ -z "$TOKEN" || -z "$ROOM_ID" ]]; then
  log "SKIP: OCTI_MATRIX_TOKEN or OPENCLAW_ROOM_ID not set"
  exit 0
fi

# Check openclaw gateway health
if ! curl -sf http://127.0.0.1:18789/ >/dev/null 2>&1; then
  log "SKIP: openclaw gateway not reachable"
  exit 0
fi

# Check ollama is serving
if ! curl -sf http://127.0.0.1:11434/api/tags >/dev/null 2>&1; then
  log "SKIP: ollama not reachable"
  exit 0
fi

# ── Task rotation ────────────────────────────────────────────────────
TASK_TYPE="${1:-rotate}"

if [[ "$TASK_TYPE" == "rotate" ]]; then
  # Rotate through task types based on hour
  HOUR=$(date +%H)
  case $((HOUR % 5)) in
    0) TASK_TYPE="groom" ;;
    1) TASK_TYPE="stale-check" ;;
    2) TASK_TYPE="audit-labels" ;;
    3) TASK_TYPE="doc-lint" ;;
    4) TASK_TYPE="wiki-synthesis" ;;
  esac
fi

# ── Build prompt ─────────────────────────────────────────────────────
REPOS=$(cd "$WORKSPACE" && ls -d */go.mod */package.json 2>/dev/null | sed 's|/.*||' | sort -u | head -10 | tr '\n' ', ' | sed 's/,$//')

case "$TASK_TYPE" in
  groom)
    PROMPT="[Octi Maintenance] Groom backlog for repos: $REPOS

Scan open GitHub issues across these repos (use gh CLI). For each repo:
1. Find issues missing labels, descriptions, or acceptance criteria
2. Add suggested labels as a comment (don't apply directly)
3. Flag duplicate issues
4. Identify issues that could be broken into smaller tasks

Focus on the 3 most impactful improvements. Be concise — one comment per issue."
    ;;

  stale-check)
    PROMPT="[Octi Maintenance] Stale issue/PR check for repos: $REPOS

Scan for staleness across these repos using gh CLI:
1. Issues with no activity for 14+ days that aren't labeled 'blocked' or 'deferred'
2. PRs with no review activity for 7+ days
3. Branches with no commits for 14+ days
4. Issues labeled 'agent:claimed' or 'agent:working' with no recent agent activity

For each finding, post a brief status comment on the issue/PR. Max 5 items."
    ;;

  audit-labels)
    PROMPT="[Octi Maintenance] Label audit for repos: $REPOS

Check label state machine consistency using gh CLI:
1. Issues with conflicting labels (e.g. both 'planned' and 'intake')
2. Issues stuck in 'agent:claimed' for >24h without progress
3. PRs that are merged but the linked issue isn't marked 'done'
4. Issues labeled 'validated' but with no linked PR

Report findings. Don't change labels — just identify inconsistencies."
    ;;

  doc-lint)
    PROMPT="[Octi Maintenance] Documentation lint for repos: $REPOS

Check documentation freshness:
1. README files that reference removed functions or outdated instructions
2. CLAUDE.md files with stale build commands or wrong paths
3. Missing or empty README in subdirectories with >5 files
4. Broken internal links in markdown files

Report the top 3 issues found with file paths and suggested fixes."
    ;;

  wiki-synthesis)
    PROMPT="[Octi Maintenance] Wiki backlog synthesis

Scan the 5 most recently modified files under /home/jared/workspace/wiki/raw/notes/ and /home/jared/workspace/wiki/concepts/ (use ls -lt and pick the newest ones).

For each file, look for:
1. Explicit TODO / FIXME / 'Next step:' / 'Open question:' markers
2. Unresolved design questions in section headings
3. Proposed features mentioned but not implemented
4. Architectural decisions flagged for follow-up

For each actionable item found, check if a matching GitHub issue already exists (use gh issue list --search). If no match:
- Open a new issue in the most relevant chitinhq/* repo (octi for orchestration, chitin for governance, wiki for docs)
- Title: short and specific, max 80 chars
- Body: include the source wiki file path and the triggering quote
- Labels: generated:wiki, tier:c (unless clearly architectural)

Rate limit: max 3 issues created per run. Skip anything that looks like speculation — only act on concrete TODOs and explicit open questions. Be conservative; humans lose trust in runaway agents."
    ;;

  *)
    log "ERROR: unknown task type: $TASK_TYPE"
    exit 1
    ;;
esac

# ── Send via Matrix ──────────────────────────────────────────────────
log "Dispatching $TASK_TYPE task"

TXN_ID="feed-$(date +%s)-$$"
GATEWAY_URL="$HOMESERVER/_matrix/client/r0/rooms/$ROOM_ID/send/m.room.message/$TXN_ID"
TASK_FLOW="swarm.feed_openclaw.task.${TASK_TYPE}.dispatched"

flow_start "$TASK_FLOW" \
  --field "task=$TASK_TYPE" \
  --field "gateway_url=$HOMESERVER" \
  --field "txn_id=$TXN_ID"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$GATEWAY_URL" \
  -X PUT \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg body "$PROMPT" '{"msgtype":"m.text","body":$body}')")

if [[ "$HTTP_CODE" -eq 200 ]]; then
  log "OK: $TASK_TYPE dispatched (txn=$TXN_ID)"
  # Record dispatch
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) task=$TASK_TYPE status=dispatched" >> "$LOG_DIR/openclaw-feed.log"
  flow_complete "$TASK_FLOW" \
    --field "task=$TASK_TYPE" \
    --field "txn_id=$TXN_ID" \
    --field "http_code=$HTTP_CODE"

  # Optional: confirm the gateway actually picked up the task.
  # Pairs with sentinel#47 — a dispatched flow without an acknowledged
  # sibling within N minutes becomes a silent-success finding.
  ACK_FLOW="swarm.feed_openclaw.task.${TASK_TYPE}.acknowledged"
  if ACK_STATUS=$(curl -sf --max-time 3 "http://127.0.0.1:18789/status" 2>/dev/null); then
    flow_complete "$ACK_FLOW" \
      --field "task=$TASK_TYPE" \
      --field "txn_id=$TXN_ID" \
      --field "gateway_status=ok"
  fi
else
  log "FAIL: Matrix returned $HTTP_CODE"
  flow_fail "$TASK_FLOW" "http_$HTTP_CODE" \
    --field "task=$TASK_TYPE" \
    --field "txn_id=$TXN_ID" \
    --field "http_code=$HTTP_CODE"
  PARENT_STATUS="failed"
  PARENT_REASON="task_${TASK_TYPE}_http_${HTTP_CODE}"
  exit 1
fi
