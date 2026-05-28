#!/usr/bin/env bash
# Reads hook input JSON from stdin, checks if a handler/controller/route file was
# modified, and injects context into Claude telling it to sync the Postman collection.
set -euo pipefail

INPUT=$(cat -)
FILE=$(echo "$INPUT" | jq -r '.tool_input.file_path // .tool_response.filePath // empty' 2>/dev/null || true)

if [ -z "$FILE" ]; then
  exit 0
fi

# Match handler, controller, and route registration files across all services
if echo "$FILE" | grep -qE "/(handler|controller|routes|router)/[^/]+\.(go|java)$"; then
  echo '{"hookSpecificOutput": {"hookEventName": "PostToolUse", "additionalContext": "A service handler/controller/route file was just modified ('$FILE'). Check postman/collections/ledger-platform.json and verify every API endpoint registered in the services is represented in the collection (correct method, path, folder, and request body). If any routes are missing, renamed, or removed, update the collection file to match the current state of the code."}}'
fi