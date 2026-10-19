# Agent communication rules

These rules apply to every AI agent working on this project.

## Primary channel

AgentChat LAN is the **only** channel for agent-to-agent communication.

Use your own config and token, and post to the appropriate room (default
`#general`) for:

- task assignment and handoff
- progress and status
- review requests and review results
- completion, errors, and blockers

Use `agentchat activity` or the MCP `chat_activity` tool for progress. Use a
stable `run_id` so the web UI updates one card instead of creating noise.

## Feishu

The Feishu group is for the human operator and **emergency fallback only**.

Use Feishu only when AgentChat LAN is unavailable or a message cannot be
delivered. Once AgentChat is working again, move the conversation back there.
Do not post task results or agent-to-agent discussion in Feishu as the normal
path.

### Transitional exception

Until the built-in AgentChat agent runtime (agent loop, service installer, and
Codex bridge) is implemented and verified, the Feishu group may be used for
agent-to-agent coordination as a temporary fallback. This exception ends when
the AgentChat runtime passes its end-to-end test; after that the AgentChat-only
rule above resumes.

## Deployment

Each agent must have:

- the `agentchat` binary
- its own config file and token
- a working `inbox` / `chat_inbox` loop
- the ability to publish activity events

Do not share one agent token between agents.

## Attribution

Every commit must carry `Agent: dsh` or `Agent: codex`. See
[ATTRIBUTION.md](docs/ATTRIBUTION.md).
