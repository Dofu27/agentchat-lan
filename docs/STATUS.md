# Status

Updated: 2026-10-19

## Done (M0)

- Go module and repository scaffold
- Wire contract v1 draft (`docs/CONTRACT.md`)
- Shared protocol types and ULID/token helpers
- Local agent config with user-only permissions
- HTTP client
- In-memory mock hub implementing the text subset
- CLI: `join`, `send`, `inbox`, `rooms`, `who`, `me`, `config`, `mock-hub`
- Tests for protocol, config, mock hub, idempotency, ack, long-poll
- CI matrix, Dockerfile, compose example, GoReleaser config
- GitHub issue/PR templates and publishing checklist
- English and Chinese README, deployment, security, contributing docs
- Codex handoff message and a copy of the repo on agent-host

## Next (M1)

- Codex: production hub, SQLite store, auth/invites, durable inbox, WebSocket
- DSH: CLI polish, contract fixtures, integration harness, docs corrections
- Joint: contract review; resolve open decisions in `docs/DECISIONS.md`

## Next (M2)

- Codex: file service, MCP server, web UI
- DSH: `push`/`pull` CLI, checksum verification, onboarding docs

## Blockers

- The admin-host shell does not have Go or Git in `PATH`; builds are currently
  done on `hub-host` with a user-local Go toolchain at `~/opt/go`.
- GitHub organization/username is not chosen yet; module path and release
  image names must be updated before publishing. Use
  `scripts/rename-module.sh`.
- Codex has not yet picked up `docs/CODEX_TASKS.md`; a copy of the M0 repo is
  at `C:\work\agentchat-lan` on agent-host. The
  ready-to-paste prompt is `docs/CODEX_HANDOFF_MESSAGE.md`.

## Verification

On hub-host:

```bash
export GOROOT="$HOME/opt/go-toolchain/usr/lib/go-1.26"
export PATH="$GOROOT/bin:$PATH"
cd ~/work/agentchat-lan
go test ./...
go build -o bin/agentchat ./cmd/agentchat
```
