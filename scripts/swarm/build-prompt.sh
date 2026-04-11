#!/usr/bin/env bash
# build-prompt.sh — assemble a task prompt from templates + issue metadata.
# No LLM calls. Pure string interpolation.
# Usage: build-prompt.sh <repo> <issue_number> <queue>
# Outputs the prompt to stdout.
set -euo pipefail

REPO="${1:?repo required}"
ISSUE_NUM="${2:?issue number required}"
QUEUE="${3:?queue required (intake|build|validate|groom)}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEMPLATE_DIR="$SCRIPT_DIR/templates"

# Fetch issue metadata once (deterministic, cached for this invocation)
ISSUE_JSON=$(gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM" 2>/dev/null) || {
  echo "ERROR: cannot fetch issue #$ISSUE_NUM from $REPO" >&2
  exit 1
}

TITLE=$(echo "$ISSUE_JSON" | jq -r '.title')
BODY=$(echo "$ISSUE_JSON" | jq -r '.body // ""')
LABELS=$(echo "$ISSUE_JSON" | jq -r '[.labels[].name] | join(", ")')

# For build/validate, also fetch the plan comment (most recent bot comment)
PLAN_COMMENT=""
if [[ "$QUEUE" == "build" || "$QUEUE" == "validate" ]]; then
  PLAN_COMMENT=$(gh api "repos/chitinhq/$REPO/issues/$ISSUE_NUM/comments" --jq '.[].body' 2>/dev/null | tail -1 || true)
fi

# For validate, fetch the PR diff
PR_DIFF=""
if [[ "$QUEUE" == "validate" ]]; then
  PR_NUM=$(gh api "repos/chitinhq/$REPO/pulls?state=open" --jq ".[] | select(.title | contains(\"#$ISSUE_NUM\") or contains(\"$TITLE\")) | .number" 2>/dev/null | head -1 || true)
  if [[ -n "$PR_NUM" ]]; then
    PR_DIFF=$(gh api "repos/chitinhq/$REPO/pulls/$PR_NUM" -H "Accept: application/vnd.github.v3.diff" 2>/dev/null | head -500 || true)
  fi
fi

# Load template and interpolate
TEMPLATE_FILE="$TEMPLATE_DIR/${QUEUE}.md"
if [[ ! -f "$TEMPLATE_FILE" ]]; then
  echo "ERROR: template not found: $TEMPLATE_FILE" >&2
  exit 1
fi

# Read template, replace placeholders
PROMPT=$(cat "$TEMPLATE_FILE")
PROMPT="${PROMPT//\{\{REPO\}\}/$REPO}"
PROMPT="${PROMPT//\{\{ISSUE_NUM\}\}/$ISSUE_NUM}"
PROMPT="${PROMPT//\{\{TITLE\}\}/$TITLE}"
PROMPT="${PROMPT//\{\{LABELS\}\}/$LABELS}"

# Multi-line replacements via temp files to avoid bash escaping issues
TEMP=$(mktemp)
echo "$PROMPT" > "$TEMP"

# Replace {{BODY}}
if [[ -n "$BODY" ]]; then
  BODY_ESCAPED=$(echo "$BODY" | sed 's/[&/\]/\\&/g; s/$/\\/' | sed '$ s/\\$//')
  sed -i "s|{{BODY}}|$BODY_ESCAPED|" "$TEMP" 2>/dev/null || true
else
  sed -i "s|{{BODY}}|No description provided.|" "$TEMP"
fi

# Replace {{PLAN}} for build/validate
if [[ -n "$PLAN_COMMENT" ]]; then
  # Write plan to a temp file and use it
  PLAN_TEMP=$(mktemp)
  echo "$PLAN_COMMENT" > "$PLAN_TEMP"
  sed -i "/{{PLAN}}/r $PLAN_TEMP" "$TEMP" 2>/dev/null || true
  sed -i "s|{{PLAN}}||" "$TEMP" 2>/dev/null || true
  rm -f "$PLAN_TEMP"
else
  sed -i "s|{{PLAN}}|No plan comment found.|" "$TEMP"
fi

# Replace {{PR_DIFF}} for validate
if [[ -n "$PR_DIFF" ]]; then
  PR_TEMP=$(mktemp)
  echo "$PR_DIFF" > "$PR_TEMP"
  sed -i "/{{PR_DIFF}}/r $PR_TEMP" "$TEMP" 2>/dev/null || true
  sed -i "s|{{PR_DIFF}}||" "$TEMP" 2>/dev/null || true
  rm -f "$PR_TEMP"
else
  sed -i "s|{{PR_DIFF}}||" "$TEMP"
fi

cat "$TEMP"
rm -f "$TEMP"
