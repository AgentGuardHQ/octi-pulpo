#!/usr/bin/env bash
# query-usage.sh — query platform usage and output normalized JSON.
# Usage: query-usage.sh <platform>
# Output: {"platform":"<name>","session_pct":<n|null>,"cycle_pct":<n|null>,"cycle_resets":"<iso>"|null,"cycle_type":"<weekly|monthly>"|null}
set -euo pipefail

PLATFORM="${1:?platform required (claude|copilot|gemini|codex)}"

case "$PLATFORM" in
  claude)
    # Claude Code /usage is interactive-only. For now, self-tracked.
    # TODO: Parse /usage output when available in headless mode.
    echo "{\"platform\":\"claude\",\"session_pct\":null,\"cycle_pct\":null,\"cycle_resets\":null,\"cycle_type\":\"weekly\"}"
    ;;
  copilot)
    echo "{\"platform\":\"copilot\",\"session_pct\":null,\"cycle_pct\":null,\"cycle_resets\":null,\"cycle_type\":null}"
    ;;
  gemini)
    echo "{\"platform\":\"gemini\",\"session_pct\":null,\"cycle_pct\":null,\"cycle_resets\":null,\"cycle_type\":null}"
    ;;
  codex)
    echo "{\"platform\":\"codex\",\"session_pct\":null,\"cycle_pct\":null,\"cycle_resets\":null,\"cycle_type\":null}"
    ;;
  *)
    echo "{\"platform\":\"${PLATFORM}\",\"session_pct\":null,\"cycle_pct\":null,\"cycle_resets\":null,\"cycle_type\":null}"
    ;;
esac
