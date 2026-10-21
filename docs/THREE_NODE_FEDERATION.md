# Three-node and multi-peer federation

Status: design for P4. F0–F3 remain experimental; no production rollout.

## Goal

Support multiple isolated trust domains without turning the hub into a relay.
Three nodes must exchange explicitly linked room messages, isolate per-peer
failures, and avoid loops.

## Topology

```text
A ── link A-B ── B
 \               /
  └── link A-C ── C

Optional links are independent. There is no transitive forwarding:
A must not carry B's messages to C.
```

Each node has:

- a stable `node_id`
- one or more peers, each with its own endpoint, TLS trust, secret, retry budget
- explicit room links keyed by `(peer, local_room, remote_room)`
- one outbox row per immutable envelope and destination peer
- one inbound receipt keyed by `(origin_node_id, origin_message_id)`

## Trust model

- A peer is authenticated by its own bearer secret and node ID.
- Imported messages use a dedicated local federation agent; remote IDs are
  never local agent foreign keys.
- A peer may submit only its own origin node ID.
- An imported message never enqueues outbound work.
- TLS verification is per peer: system roots, explicit CA, or leaf pin.
- Secrets are per peer and rotated with a coordinated restart; no global
  shared secret.
- No relay, no store-and-forward for a third node, no membership discovery.

## Config shape

Draft operator config (names are illustrative until the contract is frozen):

```json
{
  "node_id": "node-a",
  "peers": [
    {
      "id": "node-b",
      "url": "https://node-b.example:8788",
      "secret_file": "secrets/node-b",
      "tls_ca_file": "tls/ca-b.pem",
      "max_attempts": 5
    }
  ],
  "links": [
    {"peer": "node-b", "local_room": "general", "remote_room": "general"},
    {"peer": "node-c", "local_room": "general", "remote_room": "lobby"}
  ]
}
```

The current two-node config is a special case with one peer. A migration must be
additive and reject ambiguous duplicate `(peer, local_room)` links.

## Delivery and ordering

- Local insert and outbound enqueue commit in one transaction.
- Each peer has its own queue/lease; a slow or dead peer must not block another.
- Retries are per envelope and bounded; dead letters are visible in the admin
  panel.
- Remote ordering is assigned locally; no global order is promised.
- Deduplication is durable and survives queue pruning.
- Backpressure is per linked room; a full queue rejects the local send rather
  than silently dropping it.
- Imports are idempotent under replay and node restart.

## Loop and isolation tests

- A→B→A duplicate does not create a second local message.
- A→B and B→C never cause C to receive A's message from B.
- A can send to C while B is offline.
- One peer's TLS/secret failure does not stop the other peer's queue.
- Restart preserves queue state, leases, dedupe receipts, and room sequence.
- Forged origin IDs, cross-link room IDs, and unknown peers are rejected.
- Per-peer queue limits and dead-letter visibility are enforced.

## Three-node acceptance

1. Three hubs start with distinct node IDs and explicit links.
2. A sends in a linked room; B and C independently receive only where linked.
3. B goes offline; A→B queues and A→C still delivers.
4. B restarts and catches up exactly once at the application layer.
5. A duplicate/retry does not create a second message.
6. Admin outbox/dead-letter views show per-peer state.
7. No node is required to relay for another.

## Cross-platform desktop

The P2 role model is shared. Windows uses WebView2; Linux/macOS use a native
WebView or a loopback browser window with the same capability API. One installer
contract per platform; no browser extension and no cloud dependency.

## Rollout rule

Do not enable multi-peer federation on A/B before P1 has a tested rollback.
F3 typing and F4 read receipts remain opt-in/experimental until separately
accepted.
