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
- Live private-LAN deployment on `hub-host` in Docker: `agent-a`,
  `agent-b`, and `agent-c` joined `#general`; messages and a
  README file transfer were verified end-to-end with matching SHA-256.

## Done (M2b MCP, 2026-10-19)

- `internal/mcpserver` uses the official Go MCP SDK and exposes `chat_send`,
  `chat_inbox`, `chat_wait`, `chat_rooms`, `chat_upload`, `chat_download`, and
  `chat_who`.
- Tool output marks messages and file metadata as untrusted input.
- `agentchat mcp --config <path>` wires the client, config, and per-agent
  download directory; stdout is reserved for JSON-RPC and diagnostics go to
  stderr.
- `config.DownloadDirectory` defaults to `downloads/` next to the config and
  refuses paths outside that directory.
- `scripts/mcp-smoke.py` passed against the live hub: initialize, tools/list,
  `chat_rooms`, and `chat_inbox` all returned valid results.
- `docs/MCP.md` documents tools, client configs, and security rules.
- M2b merged to `main`; Linux and Windows tests pass, and the live hub smoke
  test lists all seven MCP tools.

## Done (M2c Web UI, 2026-10-19)

- `web/` contains a dependency-free HTML/CSS/JS UI embedded into the hub binary.
- `GET /` serves the shell; `GET /static/*` serves embedded assets.
- The UI logs in with an agent token, lists rooms and agents, renders messages
  with `textContent`, polls history every three seconds, and downloads file
  attachments with the Bearer header.
- It opens read-only; sending and uploading require an explicit “Enable sending
  and uploads” click.
- Strict CSP, `X-Content-Type-Options`, `Referrer-Policy`, and
  `X-Frame-Options` are applied to the UI routes.
- `TestEmbeddedUIAndCSP` covers the shell, static JS, CSP, and 404 behavior.
- M2c merged to `main`; the live private-LAN hub was redeployed with the UI.
- WebSocket live delivery and admin screens remain future work.

## Done (agent activity, post-v0.2.0)

- `meta.activity` wire object with `run_id`, `phase`, `title`, `detail`, and
  progress.
- `agentchat activity` CLI and MCP `chat_activity`.
- Web UI activity cards and Agents-panel status, updated in place by run ID.
- No schema migration: activity is durable message metadata.
- Merged to `main` as `72a616a`; ci and docker are green.
- Live LAN hub redeployed with `agentchat:m3-activity`; a working/done activity
  pair was published in `#general`.
- Not included in the `v0.2.0` release; it is the next release train.

## TLS design (merged)

- `docs/TLS.md` defines the hub certificate/key flags, CA and leaf-fingerprint
  trust, SAN/expiry/rotation rules, and shared CLI/MCP/Web UI behavior.
- DSH review decisions: the cert/key pair always selects HTTPS; CA and
  fingerprint are both supported and mutually exclusive; non-loopback HTTP
  warns now and may be refused in a future major release.
- Merged to `main` as `9b45099`.

## TLS Phase 1 (hub, done)

- `hub serve` accepts `--tls-cert` / `--tls-key`; the flags must be supplied
  together, are validated before the database or listener opens, and select
  HTTPS on any bind address.
- HTTPS requires TLS 1.2 or newer; non-loopback HTTP prints an explicit
  unencrypted-traffic warning. Without the pair, the loopback HTTP default is
  unchanged.
- Tests cover loopback/wildcard HTTP and HTTPS, the non-loopback warning,
  one-sided/missing/mismatched files, and TLS 1.0/1.1 rejection.
- Merged to `main` as `5e06624`; client CA/fingerprint trust is Phase 2.

## TLS Phase 2 (client, done)

- Config supports `tls_ca_file` and `tls_fingerprint`, mutually exclusive and
  only for HTTPS; `join` has `--ca-file` / `--fingerprint` and persists the
  chosen trust settings.
- The shared client transport uses system roots, an added CA bundle, or an
  additive leaf DER SHA-256 pin that still requires chain, SAN and validity
  checks. Redirects cannot change origin or downgrade HTTPS.
- CLI, file transfer, inbox, and MCP use the same client. `doctor` reports
  non-loopback HTTP as insecure.
- Tests cover CA trust, pin match/mismatch, unknown CA, wrong host, expired and
  not-yet-valid certificates, join persistence, HTTPS chat/files, and MCP trust.
- Live isolated HTTPS test on port 8788 passed CLI join/send/activity/files,
  MCP, Web UI assets, both CA and additive pin trust, and a Windows client;
  see [TLS-LIVE-TEST.md](TLS-LIVE-TEST.md).
- Production hub is now HTTPS: `https://agentchat.lan:8787`, with the
  internal CA published at `deploy/tls/agentchat-lan-ca.crt` and onboarding in
  [TLS-ONBOARDING.md](TLS-ONBOARDING.md).
- `agentchat onboard` prints the self-service command, and
  `agentchat join --ca-url [--ca-sha256]` downloads the CA before joining.
- Merged to `main` as `23fade3` from `17ef841`.

## Done (built-in agent runtime)

- `agentchat agent` implements a long-poll loop, self-message filtering, ack,
  optional hook JSON on stdin, reconnect/backoff, and `--once` tests. A failed
  hook leaves its message unacknowledged and stops the batch, so a later cursor
  cannot implicitly acknowledge it.
- `agentchat service install/status/uninstall` installs the same binary as a
  Windows logon task or Linux systemd user unit.
- The Windows path writes a launcher under
  `%APPDATA%\AgentChat\<task>.cmd`, avoiding the 261-character `schtasks /TR`
  limit for real config/binary paths.
- Windows logon task and Linux systemd user `install`/`status`/`uninstall`
  were both verified; the temporary Linux test unit was removed.
- `agentchat codex-bridge` auto-detects the current Codex thread from
  `thread_history_1.sqlite` and forwards through `codex queue`; an explicit
  `--thread` remains available.
- After queueing, the bridge waits for a new `thread_turns` row so a closed or
  idle Codex app is not treated as a successful handoff; failure leaves the
  AgentChat message unacknowledged for retry.
- On Windows, `--start-app` (default) starts Codex Desktop if no turn appears,
  then waits again for the turn.
- End-to-end proof in `#general`: `WORKER_OK`, `BRIDGE_AUTO_OK`,
  `FULL_LOOP_OK`, `SERVICE_LAUNCHER_OK`, `ACK_SAFE_OK`,
  `TURN_CONFIRM_OK`, `START_APP_OK`, and `SERVICE_FINAL_OK` from the installed
  logon task.
- The temporary Feishu exception is over; agent-to-agent work returns to
  AgentChat LAN.

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
python3 scripts/mcp-smoke.py --bin ./bin/agentchat --config ./agent-a.json
```

Windows/agent-host:

```powershell
cd C:\work\agentchat-lan
go test ./...
```

## Next (M3)

- DSH: TLS, release packaging, three-machine demo, GitHub release.
- Codex: WebSocket delivery, admin Web UI, and any M2 review fixes.
- Joint: contract review for the remaining open decisions, TLS, three-machine
  demo.

## Blockers / pending

- `go test -race` is not available on the Windows host (no C compiler for CGO);
  Windows runs normal `go test ./...`. Linux race tests pass.
- The private GitHub Actions result still needs a manual check in the browser;
  the deploy key can read/write Git but not the Actions API.
- Dependabot opened five action-version branches; they are intentionally not
  merged yet.
