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
- Tests for protocol, config, mock hub, idempotency, ack, long-poll, files,
  CLI flows, doctor, and history
- CI matrix with Linux race tests, Windows tests, cross-builds, and Docker build
- GitHub issue/PR templates and publishing checklist
- English and Chinese README, deployment, security, contributing docs
- Codex handoff sent on 2026-10-19; Go 1.26.8 installed user-locally on
  agent-host, and the repo copy there passes `go test ./...` on Windows
- Shared-drive snapshot, source mirror, Git bundle, and Windows binary in
  `/mnt/teamshare/agentchat`

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
- GitHub repository will be `Dofu27/agentchat-lan` and stay private until the
  M1/M2 review. The module path is already `github.com/Dofu27/agentchat-lan`.
  A repo-scoped `agentchat-deploy` SSH key was generated on hub-host; the
  first push waits for the empty private repo and that public key to be added
  with write access.
- `scripts/rename-module.sh` was tested on a temporary clone; the renamed tree
  builds and passes tests.
- The private GitHub repo `Dofu27/agentchat-lan` has not been created yet, so
  the configured `origin` cannot be pushed. Create it and add the
  `agentchat-deploy` public key with write access.
- Codex received two handoff messages on 2026-10-19 (Feishu) and has a working
  Go toolchain, but has not pushed a first `internal/store` or `internal/hub`
  commit yet. A copy of the repo is at
  `C:\work\agentchat-lan` on agent-host.

## Verification

On hub-host:

```bash
export GOROOT="$HOME/opt/go-toolchain/usr/lib/go-1.26"
export PATH="$GOROOT/bin:$PATH"
cd ~/work/agentchat-lan
go test -race ./...
go build -o bin/agentchat ./cmd/agentchat
```

On agent-host:

```powershell
cd C:\work\agentchat-lan
go test ./...
```
