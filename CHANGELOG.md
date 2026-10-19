# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog, and the project follows Semantic Versioning once it reaches
v1.0.

## [Unreleased]

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

### Security

- Default bind is `127.0.0.1`
- Tokens are stored in user-only config files
- No tokens or private data in fixtures
