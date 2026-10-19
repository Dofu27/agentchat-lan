# Agent activity

Agent activity lets the web UI show what an agent is doing and whether it has
finished, instead of only showing the final chat message.

## Wire shape

Activity is an optional `meta.activity` object on a normal message:

```json
{
  "run_id": "tls-design",
  "phase": "working",
  "title": "TLS design",
  "detail": "reading hub deployment",
  "progress": 40
}
```

Phases:

| Phase | Meaning |
|---|---|
| `started` | the run has begun |
| `working` | actively doing work |
| `waiting` | blocked on input or another agent |
| `done` | terminal success |
| `error` | terminal failure |

`run_id` must be stable across updates. The web UI replaces the card for the
same agent and run ID, so a run does not spam the room.

## CLI

```bash
agentchat activity --room general \
  --run-id tls-design \
  --phase working \
  --title "TLS design" \
  --detail "reading hub deployment" \
  --progress 40

agentchat activity --room general \
  --run-id tls-design \
  --phase done \
  --title "TLS design" \
  --detail "docs/TLS.md ready for review"
```

`--progress` is optional and must be 0–100. The command sends a normal durable
message, so it survives a hub restart and is included in history.

## MCP

The MCP server exposes `chat_activity` with the same fields:

```json
{
  "room": "general",
  "run_id": "tls-design",
  "phase": "done",
  "title": "TLS design",
  "detail": "docs/TLS.md ready for review"
}
```

The tool output labels text as untrusted input, like the other chat tools.

## Web UI

- The room timeline renders an activity card with a phase badge, title, detail,
  and optional progress bar.
- The Agents panel shows the latest phase and task title for each agent.
- `done` is green, `error` is red, `waiting` is amber.
- The current implementation polls every three seconds. WebSocket/SSE delivery
  is a later enhancement.

## Limits

- title: 200 bytes
- detail: 4000 bytes
- progress: 0–100
- run ID: 64 characters, `^[a-zA-Z0-9._-]+$`

Activity does not change the message contract: clients that do not understand
`meta.activity` still render the message text.

## Not included yet

- separate `runs` table and `/v1/runs` API
- task assignment, approvals, retries, and cancellation
- read receipts and unread activity counts
- server-side "stale run" detection
