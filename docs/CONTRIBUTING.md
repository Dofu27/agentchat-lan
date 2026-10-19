# Contributing to AgentChat LAN

Thanks for helping. This project is early; small, focused pull requests are
easier to review than large rewrites.

## Development setup

Requirements:

- Go 1.24+
- Git
- Optional: Docker for container testing

```bash
git clone https://github.com/<your-org>/agentchat-lan.git
cd agentchat-lan
go build -o bin/agentchat ./cmd/agentchat
go test ./...
```

Run the mock hub:

```bash
./bin/agentchat mock-hub --listen 127.0.0.1:8787
```

## Before you open a PR

1. Read `docs/CONTRACT.md` if you touch the wire protocol.
2. Run `gofmt -w .`, `go vet ./...`, and `go test -race ./...`.
3. Add a test for every behavior change.
4. Update docs and examples when flags or defaults change.
5. Do not commit secrets, tokens, private IPs, or personal paths.
6. Keep new dependencies rare and justified in the PR description.

## Branch and commit style

- Branch from `main`: `feat/...`, `fix/...`, `docs/...`.
- Commit messages: imperative mood, short subject, body for *why*.
- Keep the diff scoped; do not reformat unrelated files.
- Do not force-push `main`.

## Contract changes

The protocol is the hardest part to change later. For any of these, open a
draft PR against `docs/CONTRACT.md` first:

- new or renamed JSON field
- new endpoint or status code
- changed default limit
- changed auth or token format
- changed inbox/ack semantics

Add or update fixtures in `test/contract`.

## Code style

- Prefer the standard library.
- Use `context.Context` for I/O and database calls.
- Wrap errors with `%w` and enough context to debug.
- Do not log tokens, full message bodies, or file contents.
- Keep exported identifiers documented.
- Tests should be deterministic and not depend on the host network.

## Security reports

Do not open a public issue for a vulnerability. See `docs/SECURITY.md`.

## License

By contributing, you agree that your contribution is licensed under the MIT
License in `LICENSE`.
