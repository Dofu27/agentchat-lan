#!/usr/bin/env bash
# End-to-end smoke test for the production hub and CLI.
set -euo pipefail

BIN="${BIN:-./bin/agentchat}"
PORT="${PORT:-18791}"
DATA="${DATA:-/tmp/agentchat-hub-smoke}"
URL="http://127.0.0.1:${PORT}"
TOKEN_FILE="${TOKEN_FILE:-/tmp/agentchat-hub-admin-token}"
CFG_A="/tmp/agentchat-hub-a.json"
CFG_B="/tmp/agentchat-hub-b.json"
LOG="/tmp/agentchat-hub-smoke.log"
PID=""

cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$DATA" "$TOKEN_FILE" "$CFG_A" "$CFG_B"
}
trap cleanup EXIT

rm -rf "$DATA" "$TOKEN_FILE" "$CFG_A" "$CFG_B"
"$BIN" hub init --data "$DATA" --admin-token-file "$TOKEN_FILE" --json
INVITE_JSON="$("$BIN" hub invite --data "$DATA" --room general --ttl 1h --uses 2 --json)"
printf '%s\n' "$INVITE_JSON"
CODE="$(printf '%s' "$INVITE_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["code"])')"
if [[ -z "$CODE" ]]; then
  echo "could not parse invite code" >&2
  exit 1
fi

start_hub() {
  "$BIN" hub serve --data "$DATA" --listen "127.0.0.1:${PORT}" >"$LOG" 2>&1 &
  PID=$!
  for _ in $(seq 1 100); do
    if curl -fsS "${URL}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  echo "hub did not become healthy" >&2
  cat "$LOG" >&2 || true
  exit 1
}

stop_hub() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    PID=""
  fi
}

start_hub
AGENTCHAT_CONFIG="$CFG_A" "$BIN" join "$URL" --code "$CODE" --name agent-a --kind test
AGENTCHAT_CONFIG="$CFG_B" "$BIN" join "$URL" --code "$CODE" --name agent-b --kind test

AGENTCHAT_CONFIG="$CFG_A" "$BIN" send --room general --text "hello @agent-b from a"
INBOX_B="$(AGENTCHAT_CONFIG="$CFG_B" "$BIN" inbox --wait 5 --json --ack)"
printf '%s\n' "$INBOX_B"
if ! printf '%s' "$INBOX_B" | grep -q "hello @agent-b from a"; then
  echo "agent-b did not receive the message" >&2
  exit 1
fi

# Send while agent-b is not reading, restart the hub, then prove the backlog is durable.
AGENTCHAT_CONFIG="$CFG_A" "$BIN" send --room general --text "durable backlog"
stop_hub
start_hub
INBOX_B2="$(AGENTCHAT_CONFIG="$CFG_B" "$BIN" inbox --wait 5 --json --ack)"
printf '%s\n' "$INBOX_B2"
if ! printf '%s' "$INBOX_B2" | grep -q "durable backlog"; then
  echo "agent-b did not receive the durable backlog" >&2
  exit 1
fi

"$BIN" hub audit --data "$DATA" --limit 20
echo "production hub smoke test passed"
