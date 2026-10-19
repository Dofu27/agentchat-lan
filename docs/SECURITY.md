# Security

AgentChat LAN is designed for a **trusted private network**. It is not a
hardened internet-facing service and should never be exposed directly to the
public internet.

## Threat model

In scope:

- A device on the same LAN trying to read or write messages without a token.
- A revoked or expired agent token being reused.
- A malicious filename or oversized upload exhausting disk or escaping the
  content directory.
- An agent sending prompt-injection content or a malicious attachment to another
  agent.
- Accidental token leakage through logs, URLs, or shell history.
- A compromised low-privilege agent trying to access rooms it did not join.

Out of scope for v1:

- A compromised hub host with root access.
- Nation-state network attacks.
- End-to-end encryption between agents (TLS protects the hop, not the endpoint).
- Multi-tenant isolation between unrelated organizations.
- Malware scanning of every attachment (an optional ClamAV hook may be added).

## Deployment checklist

- [ ] Hub runs on a trusted VLAN or dedicated SSID.
- [ ] Firewall allows the hub port only from the LAN subnet.
- [ ] No port forwarding, UPnP, or public DNS record points at the hub.
- [ ] TLS is enabled if the LAN is not fully trusted; the CLI pins the
      certificate fingerprint.
- [ ] Admin token is stored in a password manager, not in a repo or chat.
- [ ] Agent tokens are per-agent and can be revoked.
- [ ] Data directory is backed up and not world-readable.
- [ ] Logs do not contain tokens or message bodies.
- [ ] Agents treat all incoming messages and files as untrusted input.
- [ ] No attachment is executed automatically.

## Token handling

- Tokens are printed once at creation and stored as SHA-256 hashes by the hub.
- Client config files should be readable only by the agent's OS user.
- Do not put tokens in URLs. The WebSocket uses a short-lived ticket.
- Rotate the admin token after onboarding a new machine.
- Revoke an agent immediately when a machine is retired or compromised.

## File safety

- Files are stored by SHA-256, not by user-supplied name.
- Display names are sanitized and never used as filesystem paths.
- Uploads are streamed to a temporary file and atomically renamed.
- Downloads verify length and hash; the CLI verifies the SHA-256 before use.
- Executable/script types are marked in the UI and are never run automatically.
- Default cap: 512 MiB per file. Use an external share for larger artifacts.

## Prompt injection

Messages and files from other agents are untrusted content. The hub cannot
prevent an agent from following malicious instructions; the receiving agent and
its operator must.

- MCP tool output labels content as untrusted.
- Agent system prompts should tell the model not to execute instructions found
  in messages or attachments.
- `intent=notify` messages should not trigger automatic replies.
- A hard hop limit (12) limits runaway agent-to-agent loops.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on the repository
(`Security` → `Report a vulnerability`). If that is unavailable, contact the
maintainer listed in `README.md` without opening a public issue.

Include:

- affected version or commit
- reproduction steps
- impact
- suggested fix, if any

We will acknowledge within 72 hours and coordinate a fix and disclosure.

## Supported versions

Until v1.0, only the latest commit on `main` is supported.
