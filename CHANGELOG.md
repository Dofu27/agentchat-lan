# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog, and the project follows Semantic Versioning once it reaches
v1.0.

## [0.4.2] - 2026-10-20

### Added

- `agentchat hub backup` with a consistent SQLite snapshot and `files/` archive
- `agentchat hub restore` with path-traversal and archive-entry validation
- `agentchat hub gc` with dry-run default, message retention, and unreferenced
  file cleanup

### Security

- Backup archives are written with `0600` permissions.
- Restore rejects absolute paths, `..`, symlinks, and non-regular entries.
- GC only removes storage paths that resolve inside the data directory's
  `files/` root.

## [0.4.1] - 2026-10-20

### Fixed

- Existing SSE streams now revalidate the bearer token on every wakeup and
  heartbeat. Revoking an agent closes its stream and prevents delivery of
  messages after revocation.
- Added `TestRoomEventsRevocationClosesStream`; new connections still return
  401 after revocation.

## [0.4.0] - 2026-10-20

### Added

- Authenticated SSE room stream: `GET /v1/rooms/{room}/events`
- Web UI receives new messages immediately instead of waiting for a 3-second
  poll; a 15-second history poll remains as a fallback
- SSE integration test covering stream headers and a live message event

### Changed

- Flushing support is preserved through the request-logging writer
- Contract documents the SSE ready/message/heartbeat format

## [0.3.9] - 2026-10-20

### Added

- Web UI mention autocomplete: typing `@` opens an Agent picker with arrow
  keys, Enter/Tab selection, Esc close, and case-insensitive filtering
- Message mentions are highlighted in the transcript
- Historical mentions use the message's `mentions` field, so offline agents
  remain highlighted

### Security

- Mention text is rendered with text nodes and `textContent`; no `innerHTML`
- Names that cannot be fully parsed by the server mention grammar are excluded
  from the picker
- IME composition, empty results, blur, room changes, send, and logout close
  the picker; drafts are preserved when the server rejects a target

## [0.3.8] - 2026-10-20

### Security

- Removed the private LAN address from public docs, examples, tests, and the
  default `agentchat onboard` example; public docs use `agentchat.lan` as a
  placeholder.
- `agentchat join --ca-url` still performs the real join against the hub the
  operator passes explicitly.

## [0.3.7] - 2026-10-20

### Added

- `agentchat onboard` prints a self-service CA download + join command
- `agentchat join --ca-url` downloads a public CA over HTTPS before joining;
  `--ca-sha256` verifies the download, and the CA is saved next to the config
- Verified a live self-download join against the production HTTPS hub

## [0.3.6] - 2026-10-20

### Changed

- Production hub switched to HTTPS with an internal CA:
  `https://agentchat.lan:8787`
- Public CA committed at `deploy/tls/agentchat-lan-ca.crt`; private key stays
  outside the repository
- Added `docs/TLS-ONBOARDING.md` for new agents to fetch the CA themselves
- Added `docs/TLS-PRODUCTION.md` and `scripts/tls-preflight.sh`
- Windows operator-workstation user trust store contains the internal CA; Codex uses the
  CA file directly

### Verified

- Live TLS chat and Codex handoff: `TLS_LIVE_OK`
- HTTPS Web UI index/static assets served
- CA install on operator-workstation user trust (elevated approval used for the store write)

## [0.3.5] - 2026-10-20

### Fixed

- A failed agent hook stops the current batch before any later cursor advance,
  preventing a successful later message from implicitly acknowledging the
  failed one.
- `codex-bridge` fails closed if it cannot inspect turn history before queueing,
  instead of treating an unverified handoff as successful.

## [0.3.4] - 2026-10-20

### Added

- `codex-bridge --start-app` (Windows default) starts Codex Desktop if a queued
  message produced no new turn, then waits once more.
- Verified the installed service path with `START_APP_OK`.

## [0.3.3] - 2026-10-20

### Fixed

- `codex-bridge` now confirms a new Codex turn after queueing. A closed or idle
  app no longer looks like a successful handoff; the AgentChat message remains
  unacknowledged and is retried.
- Verified the turn-confirmed path from the installed service with
  `TURN_CONFIRM_OK`.

## [0.3.2] - 2026-10-20

### Fixed

- The built-in agent loop no longer acknowledges a message when its hook fails;
  the message remains queued and is retried after a short backoff.
- Verified the installed service path returns `ACK_SAFE_OK` over AgentChat.

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
