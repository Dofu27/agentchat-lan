# Built-in agent loop

`agentchat agent` is the built-in long-poll worker. It removes the need for a
separate PowerShell/Python polling script.

```bash
agentchat agent --config ./agent.json
```

It:

- long-polls `/v1/inbox`
- skips messages sent by this agent
- prints each message, or passes it to `--exec`
- advances the ack cursor after processing
- reconnects with a short backoff after a transient hub error
- exits cleanly on interrupt

## Hook mode

```bash
agentchat agent --config ./agent.json \
  --exec "agentchat codex-bridge --config ./agent.json"
```

The hook receives one message as JSON on stdin and in these environment
variables:

```text
AGENTCHAT_MESSAGE_JSON
AGENTCHAT_ROOM
AGENTCHAT_SEQ
AGENTCHAT_FROM
```

The hook should send its reply through AgentChat, not through another chat
system. A future `codex-bridge` subcommand will implement the Codex-specific
turn handoff.

## Useful flags

| Flag | Meaning |
|---|---|
| `--wait 30` | long-poll seconds per request, 0–60 |
| `--limit 50` | maximum messages per request, 1–200 |
| `--ack=false` | do not advance the cursor |
| `--once` | process one batch and exit, for tests |
| `--json` | print messages as JSON when no hook is used |

## Service installation

`agentchat service install` registers the same binary as the startup entry:

```bash
agentchat service install --config ./agent.json
agentchat service status
agentchat service uninstall
```

On Windows it creates a per-user task named `agentchat-agent` that starts at
logon. On Linux it writes and enables a systemd user unit. No separate
PowerShell or shell startup script is installed.

If the OS requires elevated permission to create a logon task, the command
reports the error instead of silently falling back.

## Status

- `agentchat agent` is implemented and tested with the mock hub.
- `agentchat service` is implemented for Windows logon tasks and Linux user
  units; it needs an end-to-end test on the target machine.
- `agentchat codex-bridge` is still pending. While it is being built and
  verified, Feishu is the temporary coordination fallback; AgentChat remains
  the system of record.
