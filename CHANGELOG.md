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

### Security

- Default bind is `127.0.0.1`
- Tokens are stored in user-only config files
- No tokens or private data in fixtures
