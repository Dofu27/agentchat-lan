#!/usr/bin/env bash
# Local end-to-end smoke test for the mock hub and CLI.
set -euo pipefail

BIN="${BIN:-./bin/agentchat}"
PORT="${PORT:-18787}"
CFG="${CFG:-/tmp/agentchat-smoke-config.json}"
LOG="${LOG:-/tmp/agentchat-smoke-hub.log}"
URL="http://127.0.0.1:${PORT}"

rm -f "$CFG"
"$BIN" mock-hub --listen "127.0.0.1:${PORT}" >"$LOG" 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  if curl -fsS "${URL}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

export AGENTCHAT_CONFIG="$CFG"
"$BIN" join "$URL" --name smoke --kind test
"$BIN" send --room general --text "hello from smoke"

UPFILE="/tmp/agentchat-smoke-upload.txt"
DOWNFILE="/tmp/agentchat-smoke-download.txt"
printf 'file transfer smoke test\n' >"$UPFILE"
rm -f "$DOWNFILE"
PUSH_JSON="$("$BIN" push --room general --file "$UPFILE" --text "file attached" --json)"
printf '%s\n' "$PUSH_JSON"
FILE_ID="$(printf '%s' "$PUSH_JSON" | grep -o 'fil_[0-9A-Z]*' | head -1)"
if [[ -z "$FILE_ID" ]]; then
  echo "could not find uploaded file ID" >&2
  exit 1
fi
"$BIN" pull "$FILE_ID" --out "$DOWNFILE"
cmp "$UPFILE" "$DOWNFILE"

"$BIN" inbox --wait 1 --json
"$BIN" me
"$BIN" who

rm -f "$UPFILE" "$DOWNFILE"
echo "smoke test passed"
