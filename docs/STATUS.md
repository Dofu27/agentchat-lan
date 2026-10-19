# Status

Updated: 2026-10-19

## Done (M0)

- Go module and repository scaffold
- Wire contract v1 draft (`docs/CONTRACT.md`)
- Shared protocol types and ULID/token helpers
- Local agent config with user-only permissions
- HTTP client
- In-memory mock hub implementing text plus file upload/download
- CLI: `join`, `send`, `push`, `pull`, `inbox`, `history`, `rooms`, `who`, `me`, `doctor`, `config`, `mock-hub`
- Client-side SHA-256 upload/download with atomic install
- Tests for protocol, config, mock hub, idempotency, ack, long-poll, files
- CI matrix, Dockerfile, compose example, GoReleaser config
- GitHub issue/PR templates and publishing checklist
- English and Chinese README, deployment, security, contributing docs
- Codex handoff message, a copy of the repo on agent-host, and a Feishu
  handoff sent on 2026-10-19
- Shared-drive snapshot and Windows binary in `/mnt/teamshare/agentchat`

## Next (M1)

- Codex: production hub, SQLite store, auth/invites, durable inbox, WebSocket
- DSH: review Codex's store/hub API against the contract; add black-box tests
- Joint: contract review; resolve open decisions in `docs/DECISIONS.md`

## Next (M2)

- Codex: production file service, MCP server, web UI
- DSH: onboarding docs, CLI polish, release automation

## Blockers

- The admin-host shell does not have Go or Git in `PATH`; builds are currently
  done on `hub-host` with a user-local Go toolchain at
  `~/opt/go-toolchain/usr/lib/go-1.26`.
- GitHub account name is not known yet; module path and clone URLs must be
  updated before publishing. Use `scripts/rename-module.sh`.
- Codex picked up the handoff message on 2026-10-19 (Feishu), but has not
  pushed a first `internal/store` or `internal/hub` commit yet. A copy of the
  repo is at `C:\work\agentchat-lan` on agent-host.
  The ready-to-paste prompt is `docs/CODEX_HANDOFF_MESSAGE.md`.

## Verification

On hub-host:

```bash
export GOROOT="$HOME/opt/go-toolchain/usr/lib/go-1.26"
export PATH="$GOROOT/bin:$PATH"
cd ~/work/agentchat-lan
go test -race ./...
go build -o bin/agentchat ./cmd/agentchat
```
