# Status

Updated: 2026-10-19

## Done (M0)

- Go module and repository scaffold
- Wire contract v1 draft (`docs/CONTRACT.md`)
- Shared protocol types and ULID/token helpers
- Local agent config with user-only permissions
- HTTP client
- In-memory mock hub implementing text plus file upload/download
- CLI: `join`, `send`, `push`, `pull`, `inbox`, `history`, `rooms`, `who`,
  `me`, `doctor`, `config`, `mock-hub`
- Client-side SHA-256 upload/download with atomic install
- Tests for protocol, config, mock hub, idempotency, ack, long-poll, files,
  CLI flows, doctor, and history
- CI matrix with Linux race tests, Windows tests, cross-builds, and Docker build
- GitHub issue/PR templates and publishing checklist
- English and Chinese README, deployment, security, contributing docs
- Shared-drive snapshot, source mirror, Git bundle, and Windows binary in
  `/mnt/teamshare/agentchat`

## Done (M1, 2026-10-19)

Codex implemented and DSH reviewed/integrated:

- `internal/store`: SQLite schema/migrations, WAL, foreign keys, transactional
  invite/join/message/ack/room/audit operations, hashed credentials, per-room
  sequence, idempotency, durable ack cursors, history, and inbox queries.
- `internal/hub`: contract HTTP routes for health/readiness, join, agent and
  admin auth, invites, rooms, revocation, audit, history, long-poll inbox,
  idempotent send, rate limits, request IDs, and bounded graceful shutdown.
- `internal/hub` and `internal/store` tests include two-agent delivery, restart
  durability, offline backlog, ack, idempotency, auth boundaries, rate limits,
  long-poll wakeup, and graceful shutdown.

DSH added:

- CLI wiring: `agentchat hub init`, `hub serve`, `hub invite`,
  `hub list-invites`, `hub audit`.
- `agentchat hub init --admin-token-file` writes the admin token with mode
  `0600` (POSIX) and refuses to overwrite it.
- `scripts/smoke-hub.sh`: production end-to-end test covering init, invite,
  two-agent join, send/inbox/ack, hub restart, and durable backlog.
- Module path `github.com/Dofu27/agentchat-lan`; private GitHub repo pushed,
  M1 merged to `main`, and the production smoke test passes on Linux and
  Windows.

## Done (M2a files, 2026-10-19)

Codex implemented and DSH fixed/integrated:

- `internal/hub/files.go`: streaming multipart upload, SHA-256 while writing,
  content-addressed blob install, dedupe, 512 MiB cap, sanitized filename,
  upload rate limit, `GET`/`HEAD /v1/files/{id}`, Range via `http.ServeContent`,
  `Content-Disposition`, and `X-Content-SHA256`.
- `internal/store`: `files` metadata, `attachments`, room-scoped
  `CanAccessFile`, and a migration adding the missing `files.name` column for
  databases created before M2.
- Attachment messages now resolve file IDs and return file metadata in inbox,
  history, and send responses.
- DSH wired `hub serve --data` to `FileDir=<data>/files`,
  `MaxFileSize=512 MiB`, and `--uploads-per-minute`.
- Tests cover upload/download, dedupe, sanitized traversal filename, Range,
  HEAD, unauthorized and cross-room download, oversize upload, and missing
  file field.
- `scripts/smoke-files.sh` passes end-to-end: upload, attach, receive metadata,
  download, and byte-for-byte comparison.
- M2a merged to `main`; production text and file smoke tests pass on Linux and
  Windows.
- Live private-LAN deployment on `hub-host` in Docker: `dsh-main`,
  `codex-creative`, and `openclaw-ubuntu` joined `#general`; messages and a
  README file transfer were verified end-to-end with matching SHA-256.

## Verification

Linux/hub-host:

```bash
export GOROOT="$HOME/opt/go-toolchain/usr/lib/go-1.26"
export PATH="$GOROOT/bin:$PATH"
export GOPROXY=https://goproxy.cn,direct
cd ~/work/agentchat-lan
go test -race ./...
go build -o bin/agentchat ./cmd/agentchat
bash scripts/smoke-hub.sh
bash scripts/smoke-files.sh
```

Windows/agent-host:

```powershell
cd C:\work\agentchat-lan
go test ./...
```

## Next (M2b)

- Codex: MCP stdio server and Web UI/WebSocket delivery.
- DSH: MCP client config, onboarding, file CLI polish, release automation.
- Joint: contract review for the remaining open decisions, TLS, three-machine
  demo.

## Blockers / pending

- `go test -race` is not available on the Windows host (no C compiler for CGO);
  Windows runs normal `go test ./...`. Linux race tests pass.
- The private GitHub Actions result still needs a manual check in the browser;
  the deploy key can read/write Git but not the Actions API.
- Dependabot opened five action-version branches; they are intentionally not
  merged during M1.
