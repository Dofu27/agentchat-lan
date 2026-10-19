# Codex Handoff — AgentChat LAN

This file is the working agreement between DSH (admin-host) and Codex
(agent-host). Read it together with `docs/CONTRACT.md`.

## Mission

Build a production-quality, MIT-licensed, self-hosted LAN chat and file-transfer
hub for AI agents. It must work across Windows and Linux, with one static Go
binary, and be usable by people outside our LAN after a GitHub release.

## Division of work

DSH owns the **simple, contract-shaped** work:

- `internal/protocol` — shared types and fixtures
- `internal/mockhub` — in-memory hub for protocol development
- `internal/client` — small HTTP client
- `internal/config` — local agent config
- `cmd/agentchat` — CLI wiring for join/send/inbox/rooms/who
- docs, CI, Makefile, release scripts, basic tests

Codex owns the **complex, stateful, security-sensitive** work:

- `internal/store` — SQLite schema, migrations, transactions, cursors
- `internal/hub` — production HTTP server, auth, invites, rate limits, WebSocket
- `internal/files` — streaming upload/download, hashing, quotas, Range
- `internal/mcp` — MCP stdio server and tool schemas
- `web/` — browser UI
- `test/e2e` — three-agent integration and fault injection
- `deploy/` — Docker, systemd, Windows service examples

If a task crosses the boundary, open a PR that says so instead of silently
editing the other owner's package.

## Ground rules

1. **The contract is frozen after M0 review.** Do not change a JSON field,
   endpoint, status code, or default without updating `docs/CONTRACT.md` and
   getting a review from DSH.
2. **Do not commit secrets, LAN IPs, tokens, or personal paths.** Use flags,
   environment variables, and config files.
3. **Bind to `127.0.0.1` by default.** LAN exposure must be explicit.
4. **Never auto-execute files or messages from other agents.**
5. **Every exported behavior needs a test.** Prefer table-driven Go tests.
6. **No CGO.** The release binary must be static and cross-compilable.
7. **Keep the dependency graph small.** Standard library first; justify every
   new module in the PR.
8. **Run `gofmt`, `go vet`, and `go test ./...` before pushing.**
9. **Use `context.Context` and explicit timeouts** for all network and DB work.
10. **Errors are for humans; logs are for operators.** Do not leak tokens.

## Task C1 — Production hub

Status: **MVP done** (Codex, 2026-10-19; CLI wiring by DSH). TLS,
`--public-url`, and WebSocket delivery are deferred to M2/M3; the agent inbox
uses HTTP long-poll today.

Implement `agentchat hub` with:

- `--listen`, `--data`, `--public-url`, `--tls-cert`, `--tls-key`,
  `--trusted-proxy` flags
- first-run `hub init`: generate admin token, print it exactly once, create DB
- `POST /v1/join` with one-time invite codes
- agent tokens, admin tokens, revocation
- rooms, membership, DMs
- message send/history/inbox/ack with per-room `seq`
- idempotency via `client_msg_id`
- long-poll inbox with correct wakeups and cancellation
- WebSocket events for the web UI
- rate limits, request size limits, graceful shutdown
- `/healthz`, `/readyz`, structured logs, request IDs
- audit events for join, revoke, invite, delete

Acceptance:

- Two agents can exchange messages through the real hub.
- Killing and restarting the hub preserves history and ack cursors.
- An offline agent receives backlog exactly once at the application layer.
- A duplicate `client_msg_id` does not create a second message.
- Unauthorized, cross-room, and revoked-token requests are rejected.
- `go test -race ./...` passes.

## Task C2 — SQLite store

Status: **done** (Codex, 2026-10-19).

Implement a `Store` interface with a SQLite driver and migrations.

Suggested tables:

```sql
agents(id, name, kind, token_hash, status, created_at, last_seen, revoked_at)
rooms(id, name, topic, kind, created_at, archived_at)
memberships(room_id, agent_id, role, joined_at, last_ack_seq)
messages(id, room_id, seq, sender_id, kind, text, reply_to, meta_json, created_at)
mentions(message_id, agent_id)
files(id, sha256, size, mime, storage_path, created_at)
attachments(message_id, file_id, display_name)
invites(code_hash, room_id, name, expires_at, uses_left, created_at)
audit(id, ts, actor_id, action, detail_json)
```

Requirements:

- WAL mode, foreign keys, busy timeout.
- `seq` allocation and message insert in one transaction.
- Indexes for `(room_id, seq)` and unread inbox queries.
- Migrations are forward-only and idempotent.
- Tests use a temporary database, not a shared fixture.
- No SQL string concatenation with user input.

Use `modernc.org/sqlite` (pure Go) or another CGO-free driver. Pin the version
and add it to `go.mod`.

## Task C3 — File service

Status: **next** (M2). The client and mock file path already exist.

Implement `POST /v1/files`, `GET /v1/files/{id}`, and the storage layer.

DSH has already implemented the client side (`UploadFile`, `DownloadFile`,
`agentchat push`, `agentchat pull`) and an in-memory mock file store. Your job
is the durable, streaming, range-capable server implementation.

Requirements:

- Stream multipart uploads; do not buffer 512 MiB in memory.
- Hash while writing; verify after write; atomic rename.
- Content-addressed path: `files/<sha[:2]>/<sha>`.
- Enforce 512 MiB default cap and per-agent quota.
- Support `Range` and `HEAD`.
- Sanitize display names; never use them as paths.
- Reject path traversal, symlinks, and non-regular files.
- Deduplicate by SHA-256; reference counting or safe GC.
- Mark executable/script MIME types in the UI; never execute.
- Tests: oversized upload, duplicate upload, interrupted upload, range request,
  traversal filename, unauthorized download.

## Task C4 — MCP server

Implement `agentchat mcp` as a stdio MCP server.

Tools:

- `chat_send`
- `chat_inbox`
- `chat_wait`
- `chat_rooms`
- `chat_upload`
- `chat_download`
- `chat_who`

Requirements:

- Tool descriptions explicitly say that message content is untrusted.
- Return structured JSON, not shell-quoted strings.
- Respect config/profile selection (`--config`).
- Timeouts and cancellation.
- Never auto-download to a path outside the configured download directory.
- Include tested config snippets for Claude Code, Codex, and Cursor in
  `docs/ONBOARDING.md`.

## Task C5 — Web UI

A small, dependency-light web UI embedded into the hub binary.

Screens:

- room list + unread counts
- message timeline with markdown (sanitized), mentions, file cards
- send box, drag-and-drop upload
- agent presence
- admin: invites, agents, rooms, storage usage, audit

Requirements:

- No CDN dependencies; everything embedded for offline LAN use.
- WebSocket reconnect with exponential backoff.
- XSS-safe rendering; no `innerHTML` with untrusted content.
- Accessible keyboard navigation and mobile layout.
- Clear "untrusted content" warning for agent messages.
- Playwright smoke test or a documented manual checklist.

## Task C6 — Packaging and E2E

- Multi-stage `Dockerfile` producing a non-root, distroless image.
- `docker-compose.yml` with a named data volume and loopback/LAN examples.
- GoReleaser config for linux/amd64, linux/arm64, windows/amd64, darwin/amd64,
  darwin/arm64.
- GitHub Actions: test, vet, build matrix, release on tag.
- `test/e2e` harness that starts a hub on an ephemeral port and runs three
  simulated agents.
- Demo script: two agents exchange text and a file; one goes offline and catches
  up; hub restarts; checksums match.

## Build and test

```bash
go build -o bin/agentchat ./cmd/agentchat
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
```

On hub-host, the Go toolchain is currently extracted under
`~/opt/go` (not on PATH). Use:

```bash
export PATH="$HOME/opt/go/bin:$PATH"
```

## Handoff protocol

- Branch from `main`: `feat/hub-core`, `feat/files`, `feat/mcp`, `feat/web`.
- Open a PR with: what changed, contract impact, tests run, screenshots for UI.
- If you need a contract change, open a draft PR against `docs/CONTRACT.md`
  first and mention DSH.
- Update `docs/STATUS.md` with done / next / blockers.
- Do not force-push `main`.
- After M1, use `agentchat` itself in `#dev` for daily coordination.

## Definition of done

A task is done when:

- the contract is unchanged or explicitly updated;
- tests cover the happy path and at least one failure path;
- `go test -race ./...` and `go vet ./...` pass;
- docs and examples are updated;
- no secrets or machine-specific paths are committed;
- the PR has a clear rollback note if it touches storage or auth.
