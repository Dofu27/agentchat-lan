# Decisions

Short architecture decision records. Add a new entry when a decision is hard to
reverse or affects the wire contract.

## D-001 — Go, single static binary

**Status:** accepted (2026-10-19)

Use Go for the hub, CLI, and MCP server. Build with `CGO_ENABLED=0` and ship
one binary per platform. Reasons:

- three machines, two operating systems, no guaranteed language runtime
- simple cross-compilation and Docker images
- strong standard library for HTTP, TLS, and streaming files
- one language for hub, CLI, and MCP

Rejected: TypeScript (runtime distribution), Python (runtime and packaging),
Rust (slower iteration for this team).

## D-002 — Long-poll for agents, WebSocket for the UI

**Status:** accepted

Agents often run as short-lived processes. They should not be required to keep a
WebSocket open. The agent transport is `GET /v1/inbox` with durable storage,
cursor/ack, and at-least-once delivery. The browser uses WebSocket for live
updates.

Rejected: agent WebSocket as the primary transport (reconnect complexity,
offline delivery still needs a database).

## D-003 — 512 MiB file cap; external share for larger files

**Status:** accepted

Stream uploads up to 512 MiB with SHA-256 verification. Larger artifacts use the
existing SMB/SCP share and are referenced by path + hash in a message. This keeps
the first version simple and avoids building a resumable upload protocol before
it is needed.

Revisit when users routinely hit the cap.

## D-004 — Hub on a small always-on Linux host

**Status:** accepted for our deployment

Run the hub on `hub-host` in Docker, with a data volume and a LAN-only
firewall rule. The software itself is host-agnostic; the README documents Docker,
systemd, and Windows.

## D-005 — MIT license

**Status:** accepted

MIT is the simplest permissive license for a small tool and encourages reuse.
Keep the copyright line generic ("AgentChat LAN contributors") until a legal
entity is chosen.

## D-006 — Loopback bind by default

**Status:** accepted

`agentchat hub` binds `127.0.0.1:8787` unless `--listen` is given. This prevents
an accidental LAN exposure on first run. The deployment docs show the explicit
LAN command.

## D-007 — No cloud dependency

**Status:** accepted

No telemetry, no hosted service, no CDN assets in the web UI. The hub must work
on an isolated LAN. This is a product requirement, not just a deployment detail.

## Open decisions

- DM rooms: lazy creation vs explicit creation.
- Global inbox cursor vs per-room ack only.
- Observer role for the web UI vs admin-only.
- Whether to add an optional ClamAV hook in v1.
