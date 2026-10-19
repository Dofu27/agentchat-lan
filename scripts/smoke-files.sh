#!/usr/bin/env bash
# M2 acceptance: production file upload, message attachment, download, checksum.
# This will fail until the durable file service lands.
set -euo pipefail

BIN="${BIN:-./bin/agentchat}"
PORT="${PORT:-18794}"
DATA="${DATA:-/tmp/agentchat-files-smoke}"
URL="http://127.0.0.1:${PORT}"
TOKEN_FILE="/tmp/agentchat-files-admin.token"
CFG_A="/tmp/agentchat-files-a.json"
CFG_B="/tmp/agentchat-files-b.json"
LOG="/tmp/agentchat-files-smoke.log"
PID=""
SRC="/tmp/agentchat-files-src.txt"
DST="/tmp/agentchat-files-dst.txt"

cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$DATA" "$TOKEN_FILE" "$CFG_A" "$CFG_B" "$SRC" "$DST"
}
trap cleanup EXIT

rm -rf "$DATA" "$TOKEN_FILE" "$CFG_A" "$CFG_B" "$SRC" "$DST"
"$BIN" hub init --data "$DATA" --admin-token-file "$TOKEN_FILE" --json >/dev/null
INVITE_JSON="$("$BIN" hub invite --data "$DATA" --ttl 1h --uses 2 --json)"
CODE="$(printf '%s' "$INVITE_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["code"])')"
printf 'file transfer acceptance\n' >"$SRC"

"$BIN" hub serve --data "$DATA" --listen "127.0.0.1:${PORT}" >"$LOG" 2>&1 &
PID=$!
for _ in $(seq 1 100); do
  if curl -fsS "${URL}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

AGENTCHAT_CONFIG="$CFG_A" "$BIN" join "$URL" --code "$CODE" --name file-a --kind test >/dev/null
AGENTCHAT_CONFIG="$CFG_B" "$BIN" join "$URL" --code "$CODE" --name file-b --kind test >/dev/null

PUSH_JSON="$(AGENTCHAT_CONFIG="$CFG_A" "$BIN" push --room general --file "$SRC" --text "file acceptance" --json)"
printf '%s\n' "$PUSH_JSON"
FILE_ID="$(printf '%s' "$PUSH_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["files"][0]["id"])')"
if [[ -z "$FILE_ID" ]]; then
  echo "could not parse file id" >&2
  exit 1
fi

AGENTCHAT_CONFIG="$CFG_B" "$BIN" pull "$FILE_ID" --out "$DST"
cmp "$SRC" "$DST"

INBOX_B="$(AGENTCHAT_CONFIG="$CFG_B" "$BIN" inbox --wait 5 --json --ack)"
printf '%s\n' "$INBOX_B"
if ! printf '%s' "$INBOX_B" | grep -q "$FILE_ID"; then
  echo "agent-b inbox did not contain the attachment" >&2
  exit 1
fi

echo "production file smoke test passed"
