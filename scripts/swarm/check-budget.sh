#!/usr/bin/env bash
# check-budget.sh — deterministic budget check before dispatch.
# Reads dispatch log, enforces daily/weekly caps without any LLM calls.
# Usage: check-budget.sh <platform>
# Exits 0 if dispatch is allowed, 1 if budget exhausted.
set -euo pipefail

PLATFORM="${1:?platform required (copilot|claude)}"
LOG_DIR="${OCTI_LOG_DIR:-$HOME/.local/share/octi/swarm}"
LOG_FILE="$LOG_DIR/dispatch.log"

mkdir -p "$LOG_DIR"
touch "$LOG_FILE"

# Config (matches octi-config.json)
COPILOT_DAILY_CAP=8
CLAUDE_DAILY_CAP=6
COPILOT_COOLDOWN_MIN=30
CLAUDE_COOLDOWN_MIN=45
WEEKLY_CAP_PCT=70  # of 7-day theoretical max

TODAY=$(date -u +%Y-%m-%d)
WEEK_START=$(date -u -d "last monday" +%Y-%m-%d 2>/dev/null || date -u -v-monday +%Y-%m-%d 2>/dev/null || echo "$TODAY")
NOW_EPOCH=$(date +%s)

case "$PLATFORM" in
  copilot)
    DAILY_CAP=$COPILOT_DAILY_CAP
    COOLDOWN_SEC=$((COPILOT_COOLDOWN_MIN * 60))
    WEEKLY_MAX=$((COPILOT_DAILY_CAP * 7))
    ;;
  claude)
    DAILY_CAP=$CLAUDE_DAILY_CAP
    COOLDOWN_SEC=$((CLAUDE_COOLDOWN_MIN * 60))
    WEEKLY_MAX=$((CLAUDE_DAILY_CAP * 7))
    ;;
  *) echo "BUDGET FAIL: unknown platform $PLATFORM" >&2; exit 1 ;;
esac

WEEKLY_CAP=$(( WEEKLY_MAX * WEEKLY_CAP_PCT / 100 ))

# Count today's dispatches for this platform
DAILY_COUNT=$(grep "^$TODAY" "$LOG_FILE" | grep -c "platform=$PLATFORM" || true)
if [[ "$DAILY_COUNT" -ge "$DAILY_CAP" ]]; then
  echo "BUDGET FAIL: $PLATFORM daily cap reached ($DAILY_COUNT/$DAILY_CAP)" >&2
  exit 1
fi

# Count this week's dispatches
WEEKLY_COUNT=$(awk -v start="$WEEK_START" -v plat="platform=$PLATFORM" '$1 >= start && $0 ~ plat' "$LOG_FILE" | wc -l || true)
if [[ "$WEEKLY_COUNT" -ge "$WEEKLY_CAP" ]]; then
  echo "BUDGET FAIL: $PLATFORM weekly cap reached ($WEEKLY_COUNT/$WEEKLY_CAP)" >&2
  exit 1
fi

# Check cooldown since last dispatch
LAST_EPOCH=$(grep "platform=$PLATFORM" "$LOG_FILE" | tail -1 | awk '{print $2}' | sed 's/epoch=//' || true)
if [[ -n "$LAST_EPOCH" ]]; then
  ELAPSED=$((NOW_EPOCH - LAST_EPOCH))
  if [[ "$ELAPSED" -lt "$COOLDOWN_SEC" ]]; then
    REMAINING=$(( (COOLDOWN_SEC - ELAPSED) / 60 ))
    echo "BUDGET FAIL: $PLATFORM cooldown active ($REMAINING min remaining)" >&2
    exit 1
  fi
fi

echo "BUDGET OK: $PLATFORM daily=$DAILY_COUNT/$DAILY_CAP weekly=$WEEKLY_COUNT/$WEEKLY_CAP"
