#!/usr/bin/env bash
# emit-telemetry.sh — write a swarm dispatch event as JSONL for sentinel ingestion.
# Usage: emit-telemetry.sh <platform> <repo> <issue> <queue> <model> <result> <exit_code> <duration_ms>
# Appends one JSON line to $OCTI_TELEMETRY_DIR/swarm-events.jsonl
set -euo pipefail

PLATFORM="${1:?platform required}"
REPO="${2:?repo required}"
ISSUE_NUM="${3:?issue required}"
QUEUE="${4:?queue required}"
MODEL="${5:?model required}"
RESULT="${6:?result required}"
EXIT_CODE="${7:?exit_code required}"
DURATION_MS="${8:-0}"

TELEMETRY_DIR="${OCTI_TELEMETRY_DIR:-$HOME/.local/share/octi/telemetry}"
EVENTS_FILE="$TELEMETRY_DIR/swarm-events.jsonl"
mkdir -p "$TELEMETRY_DIR"

TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EPOCH=$(date +%s)
SESSION_ID="swarm-${EPOCH}-${PLATFORM}-${REPO}"
EVENT_ID="swarm-${PLATFORM}-${REPO}-${ISSUE_NUM}-${QUEUE}-${EPOCH}"

HAS_ERROR="false"
[[ "$RESULT" == "failed" ]] && HAS_ERROR="true"

# Write sentinel-compatible ExecutionEvent as JSONL
cat >> "$EVENTS_FILE" <<JSONEOF
{"id":"${EVENT_ID}","timestamp":"${TIMESTAMP}","source":"swarm","session_id":"${SESSION_ID}","sequence_num":1,"actor":"agent","agent_id":"${PLATFORM}","command":"swarm-${QUEUE}","arguments":["--model","${MODEL}","--repo","${REPO}","--issue","${ISSUE_NUM}"],"exit_code":${EXIT_CODE},"duration_ms":${DURATION_MS},"working_dir":"","repository":"chitinhq/${REPO}","branch":"swarm/${QUEUE}-${ISSUE_NUM}","stdout_hash":"","stderr_hash":"","has_error":${HAS_ERROR},"tags":{"platform":"${PLATFORM}","queue":"${QUEUE}","model":"${MODEL}","issue":"${ISSUE_NUM}","result":"${RESULT}"}}
JSONEOF

echo "TELEMETRY: event ${EVENT_ID} written to ${EVENTS_FILE}"
