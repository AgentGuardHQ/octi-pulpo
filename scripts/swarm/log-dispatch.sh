#!/usr/bin/env bash
# log-dispatch.sh — append a dispatch event to the log.
# Usage: log-dispatch.sh <platform> <repo> <issue_number> <queue> <model> <result>
set -euo pipefail

PLATFORM="${1:?platform required}"
REPO="${2:?repo required}"
ISSUE_NUM="${3:?issue number required}"
QUEUE="${4:?queue required}"
MODEL="${5:?model required}"
RESULT="${6:-pending}"

LOG_DIR="${OCTI_LOG_DIR:-$HOME/.local/share/octi/swarm}"
LOG_FILE="$LOG_DIR/dispatch.log"

mkdir -p "$LOG_DIR"

DATE=$(date -u +%Y-%m-%d)
EPOCH=$(date +%s)
TIME=$(date -u +%H:%M:%S)

echo "$DATE epoch=$EPOCH time=$TIME platform=$PLATFORM repo=$REPO issue=$ISSUE_NUM queue=$QUEUE model=$MODEL result=$RESULT" >> "$LOG_FILE"
