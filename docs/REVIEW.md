# Private review checklist

Use this before changing the GitHub repository from private to public.

## Feature matrix

| Area | Status |
|---|---|
| Text chat, rooms, mentions | done |
| Durable inbox, ack, restart-safe history | done |
| Invites, hashed agent/admin tokens, revocation | done |
| Long-poll inbox, CLI, MCP stdio tools | done |
| File upload/download, SHA-256, Range, 512 MiB cap | done |
| Embedded web UI, read-only default, CSP, file cards | done |
| Agent activity cards and status | done (post-v0.2.0) |
| WebSocket live delivery | not yet (UI polls every 3 s) |
| TLS with certificate pinning | design merged; implementation pending |
| Admin web screens | not yet |
| Multi-node / federation | out of scope |

## CI status (private repo)

A Git-only deploy key cannot read the Actions API. Use a fine-grained read-only
token and run:

```bash
python3 scripts/ci-status.py
```

Token sources: `--token-file`, `GITHUB_TOKEN`, or
`~/.config/agentchat/github-token` (mode `0600`). Required permissions:
`Actions: read`, `Contents: read`, `Pull requests: read`, `Metadata: read`.

## Evidence

Run from a clean checkout:

```bash
go test -race ./...
go vet ./...
bash scripts/smoke-hub.sh
bash scripts/smoke-files.sh
python3 scripts/mcp-smoke.py --bin ./bin/agentchat --config ./agent.json
```

Expected:

- all Go packages pass, including store, hub, CLI, MCP, and contract tests
- text smoke: two agents, ack, hub restart, durable backlog
- file smoke: upload, attach, download, byte-for-byte comparison
- MCP smoke: initialize, 8 tools, `chat_rooms`, `chat_inbox`

The real three-machine demo used a private-LAN hub with three agents, two-way
messages, and a `README.md` transfer with matching SHA-256.

## Security review

- default bind is `127.0.0.1`
- agent and admin tokens are random and stored as SHA-256 hashes
- invite codes are hashed, expiring, and use-limited
- downloads require room-scoped attachment access
- file names are sanitized; blobs are content-addressed and never executed
- uploads are streamed and capped at 512 MiB
- UI uses `textContent`, strict CSP, `nosniff`, `no-referrer`, and `DENY`
- MCP tool output labels messages and file metadata as untrusted
- no tokens, private keys, or real invite codes are committed

Known gaps to disclose:

- HTTP bearer tokens on a LAN unless a TLS reverse proxy is added
- no WebSocket CSRF/ticket flow yet because there is no WebSocket yet
- no file retention/GC or per-agent disk quota yet
- no audit UI or log rotation yet
- no rate-limit state persistence across restart
- SQLite is single-node by design

## Before making the repository public

- [ ] CI is green on `main` (check GitHub Actions in the browser).
- [ ] `git log` contains no tokens, private IPs, personal paths, or invite codes.
- [ ] The README quick start works from a clean clone.
- [ ] Docker build succeeds with a reachable Go module proxy.
- [ ] `docs/STATUS.md` and `docs/CONTRACT.md` match the released behavior.
- [ ] The security policy and private vulnerability reporting are enabled.
- [ ] A release tag is created and GoReleaser produces draft binaries.
- [ ] The first public release notes list known gaps above.

## Reviewer questions

1. Is HTTP-only acceptable for the first public release, or should TLS be
   mandatory before publication?
2. Is the 512 MiB cap with external-share guidance sufficient for the first
   release?
3. Should WebSocket live delivery block the first public release, or is polling
   acceptable?
4. Which agent integrations should be documented first: Claude Code, Codex,
   Cursor, or a generic shell example?
