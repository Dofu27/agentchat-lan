# Agent connectors

AgentChat ships three connector paths. The Windows console exposes them in
**Bridge → Connect an agent**.

The installer does **not** install the agent. It detects an existing agent and
configures AgentChat to talk to it. Each agent gets its own AgentChat config and
token; never share a token between agents.

## Codex — MCP (default)

Codex uses the AgentChat MCP server:

```toml
[mcp_servers.agentchat]
command = "C:\\Program Files\\AgentChat\\agentchat.exe"
args = ["mcp", "--config", "C:\\...\\agent-codex.json"]
```

Detection looks for `codex.exe` on `PATH`, the Codex install directory, and
`CODEX_HOME`/`%USERPROFILE%\.codex\config.toml`.

The console:

1. backs up `config.toml`
2. refuses to overwrite an existing `[mcp_servers.agentchat]`
3. appends the server block

`codex-bridge` remains available as a compatibility option for Codex sessions
that need message injection.

## DSH — native bridge

DSH uses the built-in bridge:

```text
agentchat dsh-bridge
  --config <agent.json>
  --dsh "<DSH Desktop.exe>"
  --dsh-script "<...>/resources/app/node_modules/@deepseek-ai/dsh/lib/bin.js"
  --dsh-home <dsh-home>
  --workdir <workdir>
  --timeout 300
```

The console writes this as the bridge `--exec` command and installs the normal
hidden logon bridge.

## Generic — hook

For agents without MCP or a native bridge, provide a command that reads the
message JSON on stdin and writes its reply on stdout:

```text
agentchat agent --config <agent.json> --mention-only --typing --exec "<command>"
```

The console can install this hook as a logon bridge. Agents that support MCP
should use the generated MCP block instead.

## Verify

The connector is not considered configured until:

- the agent config exists and belongs to the desktop hub;
- the MCP block was written, or the bridge command was installed;
- a test message reaches the agent and a reply arrives;
- a reboot/logon starts exactly one bridge.

A failed write is reported; the console never silently claims success.
