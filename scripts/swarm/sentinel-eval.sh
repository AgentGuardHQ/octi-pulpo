#!/usr/bin/env bash
# sentinel-eval.sh — run sentinel analysis on recent swarm events.
# Called after dispatch to detect failures, patterns, and anomalies.
# Usage: sentinel-eval.sh [--full]
# Default: lightweight check (failure rate + sequences only)
# --full: run all 7 detection passes
set -euo pipefail

SENTINEL_BIN="${SENTINEL_BIN:-sentinel}"
TELEMETRY_DIR="${OCTI_TELEMETRY_DIR:-$HOME/.local/share/octi/telemetry}"
EVENTS_FILE="$TELEMETRY_DIR/swarm-events.jsonl"
EVAL_DIR="$TELEMETRY_DIR/evaluations"
mkdir -p "$EVAL_DIR"

MODE="${1:-lightweight}"

if [[ ! -f "$EVENTS_FILE" ]] || [[ ! -s "$EVENTS_FILE" ]]; then
  echo "SENTINEL-EVAL: no events to analyze"
  exit 0
fi

# Count recent events (last hour)
HOUR_AGO=$(date -u -d "1 hour ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-1H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "")
RECENT_COUNT=$(wc -l < "$EVENTS_FILE")
echo "SENTINEL-EVAL: $RECENT_COUNT total events in telemetry log"

# Quick stats — no sentinel binary needed, pure bash
TOTAL=$(wc -l < "$EVENTS_FILE")
FAILURES=$(grep -c '"has_error":true' "$EVENTS_FILE" || true)
SUCCESSES=$((TOTAL - FAILURES))

if [[ "$TOTAL" -gt 0 ]]; then
  FAILURE_RATE=$((FAILURES * 100 / TOTAL))
else
  FAILURE_RATE=0
fi

# Per-platform stats
CLAUDE_TOTAL=$(grep -c '"platform":"claude"' "$EVENTS_FILE" || true)
CLAUDE_FAIL=$(grep '"platform":"claude"' "$EVENTS_FILE" | grep -c '"has_error":true' || true)
COPILOT_TOTAL=$(grep -c '"platform":"copilot"' "$EVENTS_FILE" || true)
COPILOT_FAIL=$(grep '"platform":"copilot"' "$EVENTS_FILE" | grep -c '"has_error":true' || true)

# Per-queue stats
INTAKE_FAIL=$(grep '"queue":"intake"' "$EVENTS_FILE" | grep -c '"has_error":true' || true)
BUILD_FAIL=$(grep '"queue":"build"' "$EVENTS_FILE" | grep -c '"has_error":true' || true)
VALIDATE_FAIL=$(grep '"queue":"validate"' "$EVENTS_FILE" | grep -c '"has_error":true' || true)

# Per-model stats
MODEL_STATS=$(jq -r '.tags.model' "$EVENTS_FILE" 2>/dev/null | sort | uniq -c | sort -rn || true)

EVAL_FILE="$EVAL_DIR/eval-$(date +%s).json"

cat > "$EVAL_FILE" <<EVALEOF
{
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "total_events": $TOTAL,
  "successes": $SUCCESSES,
  "failures": $FAILURES,
  "failure_rate_pct": $FAILURE_RATE,
  "platforms": {
    "claude": {"total": $CLAUDE_TOTAL, "failures": $CLAUDE_FAIL},
    "copilot": {"total": $COPILOT_TOTAL, "failures": $COPILOT_FAIL}
  },
  "queue_failures": {
    "intake": $INTAKE_FAIL,
    "build": $BUILD_FAIL,
    "validate": $VALIDATE_FAIL
  },
  "alerts": []
}
EVALEOF

# Generate alerts
ALERTS=()

if [[ "$FAILURE_RATE" -gt 50 && "$TOTAL" -gt 4 ]]; then
  ALERTS+=("HIGH: Overall failure rate ${FAILURE_RATE}% (${FAILURES}/${TOTAL})")
fi

if [[ "$CLAUDE_FAIL" -gt 0 && "$CLAUDE_TOTAL" -gt 0 ]]; then
  CLAUDE_RATE=$((CLAUDE_FAIL * 100 / CLAUDE_TOTAL))
  if [[ "$CLAUDE_RATE" -gt 60 ]]; then
    ALERTS+=("WARN: Claude failure rate ${CLAUDE_RATE}% — check auth or model availability")
  fi
fi

if [[ "$COPILOT_FAIL" -gt 0 && "$COPILOT_TOTAL" -gt 0 ]]; then
  COPILOT_RATE=$((COPILOT_FAIL * 100 / COPILOT_TOTAL))
  if [[ "$COPILOT_RATE" -gt 60 ]]; then
    ALERTS+=("WARN: Copilot failure rate ${COPILOT_RATE}% — check rate limits or model")
  fi
fi

if [[ "$BUILD_FAIL" -gt 3 ]]; then
  ALERTS+=("WARN: Build queue has $BUILD_FAIL failures — check test infrastructure")
fi

# Detect repeated failures on same issue (stuck pattern)
STUCK_ISSUES=$(jq -r 'select(.has_error == true) | .tags.issue' "$EVENTS_FILE" 2>/dev/null | sort | uniq -c | sort -rn | awk '$1 >= 3 {print $2}' || true)
if [[ -n "$STUCK_ISSUES" ]]; then
  ALERTS+=("CRITICAL: Issues stuck after 3+ failures: $STUCK_ISSUES — escalate to human")
fi

# Output summary
echo ""
echo "=== SENTINEL EVAL ==="
echo "Events: $TOTAL total, $SUCCESSES ok, $FAILURES failed ($FAILURE_RATE% failure rate)"
echo "Claude: $CLAUDE_TOTAL dispatches, $CLAUDE_FAIL failures"
echo "Copilot: $COPILOT_TOTAL dispatches, $COPILOT_FAIL failures"
echo ""

if [[ ${#ALERTS[@]} -gt 0 ]]; then
  echo "ALERTS:"
  for alert in "${ALERTS[@]}"; do
    echo "  - $alert"
  done

  # Send critical alerts to ntfy
  NTFY_TOPIC="${NTFY_TOPIC:-ganglia}"
  for alert in "${ALERTS[@]}"; do
    if [[ "$alert" == CRITICAL* || "$alert" == HIGH* ]]; then
      curl -s -d "$alert" "https://ntfy.sh/$NTFY_TOPIC" \
        -H "Title: Swarm Alert" \
        -H "Priority: high" \
        -H "Tags: robot,warning" >/dev/null 2>&1 || true
    fi
  done
else
  echo "No alerts — swarm is healthy"
fi

echo "Evaluation saved: $EVAL_FILE"

# If --full requested and sentinel binary available, run full analysis
if [[ "$MODE" == "--full" ]] && command -v "$SENTINEL_BIN" >/dev/null 2>&1; then
  echo ""
  echo "Running full sentinel analysis..."
  "$SENTINEL_BIN" analyze 2>&1 || echo "WARN: sentinel analyze failed"
fi
