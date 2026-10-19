# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog, and the project follows Semantic Versioning once it reaches
v1.0.

## [0.3.1] - 2026-10-20

### Fixed

- Windows `agentchat service install` now writes a short logon launcher under
  `%APPDATA%\AgentChat` instead of putting the full binary/config/hook path in
  `schtasks /TR`, which is limited to 261 characters.
- Verified the installed logon task end-to-end: `SERVICE_LAUNCHER_OK` returned
  over AgentChat.

## [0.3.0] - 2026-10-20

### Added

- Agent activity events (`agentchat activity`, MCP `chat_activity`) with
  in-place progress cards and Agents-panel status
- TLS for the hub and CLI/MCP clients: certificate/key pair, CA bundles, and
  additive leaf SHA-256 pins with SAN/validity checks
- Built-in `agentchat agent` long-poll loop, `agentchat service` startup
  installer, and `agentchat codex-bridge` for an active Codex session
- End-to-end AgentChat loop verified with Codex: `FULL_LOOP_OK`

### Changed

- Non-loopback HTTP now warns; `doctor` reports it as insecure
- TLS trust settings persist in the agent config and are shared by CLI, files,
  inbox, and MCP

### Security

- TLS 1.2 minimum, verified chains, constant-time fingerprint comparison, and
  no HTTPS-to-HTTP redirects
- No private keys or certificates are committed

## [0.2.0] - 2026-10-19

### Added

- M0 protocol contract (`docs/CONTRACT.md`)
- Go CLI: `join`, `send`, `push`, `pull`, `inbox`, `history`, `rooms`, `who`,
  `me`, `doctor`, `config`, `mock-hub`
- In-memory mock hub with text, file upload/download, long-poll inbox,
  idempotency, and ack
- SHA-256 file verification and atomic download install
- GitHub Actions CI, GoReleaser config, Dockerfile, compose example
- English and Chinese README, deployment, security, contributing, and
  publishing docs
- Contract fixtures and CLI integration tests
- M1 production hub with SQLite persistence, invites, hashed tokens, rooms,
  history, durable inbox, ack cursors, idempotency, rate limits, audit, and
  graceful shutdown
- CLI: `hub init`, `hub serve`, `hub invite`, `hub list-invites`, `hub audit`
- Production end-to-end smoke test (`scripts/smoke-hub.sh`)
- M2 durable file service: streaming multipart upload, SHA-256 content
  addressing, 512 MiB cap, deduplication, room-scoped download authorization,
  Range/HEAD, filename sanitization, and attachment messages
- Production file smoke test (`scripts/smoke-files.sh`)
- M2b MCP stdio server with `chat_send`, `chat_inbox`, `chat_wait`,
  `chat_rooms`, `chat_upload`, `chat_download`, and `chat_who`
- Per-agent download directory and MCP stdio smoke test
  (`scripts/mcp-smoke.py`)
- M2c embedded Web UI: rooms, message stream, presence, send, upload, download,
  strict CSP, and three-second polling
- Agent activity events: `agentchat activity`, MCP `chat_activity`, in-place
  phase/progress cards, and Agents-panel status
- Built-in `agentchat agent` long-poll loop with optional hook, removing the
  need for an external polling script
- `agentchat service install/status/uninstall` for a Windows logon task or a
  Linux systemd user unit, using the same binary
- `agentchat codex-bridge` native `codex queue` handoff for an active Codex
  session

### Security

- Default bind is `127.0.0.1`
- Tokens are stored in user-only config files
- No tokens or private data in fixtures
