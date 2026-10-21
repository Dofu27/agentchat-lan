# Program roadmap

Updated: 2026-10-21

This is the master delivery plan after the DSH bridge work. The product is one
Go binary, one self-hosted LAN hub, and one cross-platform desktop entry point.

## Current baseline

- `v0.21.11` (`75336ad`): DSH bridge, one-time fixed-cutoff start, OS-scoped
  initialization lock, Startup fallback, and Windows uninstall fixes.
- Codex review: `REVIEW_OK` for `75336ad` at AgentChat `#354`.
- Live DSH bridge: real headless reply `DSH_2111_OK` at `#349`; restart reply
  `DSH_RESTART_OK` at `#335`.
- Production hubs A and B remain on `v0.14.1`.
- Federation F0–F3 is experimental and not part of the production rollout.
- F4 explicit local read receipts is implemented in `v0.22.0` (experimental;
  no federation forwarding yet).

## P0 — Live agent runtime (done)

Acceptance:

- A real DSH headless turn produces an AgentChat reply without a polling script.
- A bridge restart does not replay historical room traffic.
- A failed DSH turn leaves its message unacknowledged and retryable.
- Windows Task Scheduler fallback reaches the same launcher at logon.

Status: `v0.21.11`, Codex `REVIEW_OK`.

## P1 — Production hardening and rollout

Runbook: [PRODUCTION_ROLLOUT.md](PRODUCTION_ROLLOUT.md).

1. Verify the current release tag, CI matrix, checksums, and release assets.
2. Record a full logon/reboot acceptance for the DSH bridge and one other
   bridge; verify no duplicate bridge process.
3. Back up A and B, then stage the smallest production upgrade that contains
   only the accepted features. Keep federation, F4, and experimental desktop
   features disabled.
4. Verify TLS, logs, files, MCP, agent runtime, and rollback on A, then B.
5. Update `docs/STATUS.md` with exact versions, rollback commands, and operator
   evidence.

Exit criteria: A and B run an explicitly chosen stable version, both can roll
back, and the DSH bridge stays live through a host restart.

## P2 — Unified desktop (in progress)

Design: [UNIFIED_DESKTOP.md](UNIFIED_DESKTOP.md). The v0.22.8 desktop has
single-instance, Chat/Join, Bridge, role tabs, and a self-contained per-user
installer EXE; local Join E2E and elevated hub-service start (`/healthz` 200)
passed. Remaining: reboot acceptance and installer Startup/Start Menu checks.

One `AgentChatConsole` entry point with role-aware tabs:

- Hub: setup, start/stop, health, TLS, data directory, logs
- Client: join, rooms, chat, files, settings
- Bridge: agent identity, hook, mention-only, typing, start-now, logs
- Admin: invites, agents, rooms, storage, audit
- Federation: links, outbox, dead letters, read-only status
- Logs: Hub/client/bridge JSONL with source switch and pause
- Single installer, no browser required, no separate polling script
- Single-instance guard and an explicit "run at login" switch

Exit criteria: a new Windows machine can install once, configure a role, and run
without a terminal.

## P3 — F4 explicit read receipts (local stage done, opt-in)

Design and implement explicit read state separately from delivery ACK.

- Local `POST /v1/rooms/{room}/read` with monotonic per-agent read cursor
- Read state never advances the delivery cursor and never acknowledges a message
- `GET /v1/rooms/{room}/receipts` returns delivery and read state distinctly
- ACL, rate limit, bounds, and audit follow existing room-message rules
- Federation receipt forwarding is opt-in, bounded, and disabled by default
- Tests cover replay, stale/backwards seq, revoked tokens, cross-room access,
  restart durability, and federation loop prevention

Exit criteria: read state is useful, cannot silently consume an inbox item, and
remains disabled unless the operator opts in.

## P4 — Federation and cross-platform

Design: [THREE_NODE_FEDERATION.md](THREE_NODE_FEDERATION.md).

- Promote F0–F3 only after P1 has a stable rollback path.
- F4 read receipts after P3.
- Three nodes, at least two peers, with explicit trust domains and link maps.
- Windows, Linux, and macOS desktop builds; one installer contract per platform.
- HA and exactly-once claims stay out of scope.

## P5 — Open-source release

- Reproducible release artifacts and checksums
- English/Chinese README, screenshots, upgrade and backup guides
- Security notes: LAN-only default, file cap, token handling, no telemetry
- License and contribution files

## Ordering rule

Do not add a new experimental feature to A or B before P1's rollback evidence.
P2 and P3 may proceed on the experimental line in parallel; P4 waits for P1.
