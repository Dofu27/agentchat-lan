# Two-node federation PoC

## Operator operations

The following routes are registered **only** when `hub serve
--federation-config <path>` enables federation. With no config, the whole
operations namespace returns 404, including retry POSTs. Persisted peers do
not enable routes after restart. All enabled routes require the hub's admin
Bearer token; agent tokens and federation peer secrets are refused.

- `GET /v1/admin/federation`: `runtime_enabled`, `node_id`, `peers`,
  `room_links`, and `outbox` counts (`pending`, `inflight`, `delivered`, `dead`,
  `attempts`). Status is read in one database snapshot.
- `GET /v1/admin/federation/outbox?state=dead&limit=50`: `items`, newest first.
  States are pending/inflight/delivered/dead; default dead, limit 1–100 (default
  50). Items contain only ID, peer/origin IDs, state, attempts, safe error class
  and creation time. No payload, URL, credential/hash, or lease token is returned.
  Unexpected stored error strings become `delivery failure`.
- `POST /v1/admin/federation/outbox/{id}/retry`: requires `Idempotency-Key`
  (1–128 printable non-whitespace ASCII bytes), no body. Only dead jobs become
  pending; attempts reset to zero, lease and last error clear, next attempt is
  immediately due. The payload, origin IDs and configured retry budget stay the
  same. Remote deduplication still applies if an earlier ACK was lost.

Retry returns `{ "id": "…", "replayed": false }`, or `replayed: true` for
the same job/key even if a worker has since changed its state. Repeated keys
acknowledge the original operation; they do not promise a job is still pending.
Use a fresh key for a deliberate new retry after another terminal failure.
A fresh key on pending/inflight/delivered returns 409 `not_dead`; an unknown
job returns 404 `not_found`; absent/invalid keys return 400. State change,
hashed-key receipt and `federation.outbox_retried` audit event commit atomically.
Concurrent duplicate requests cause one transition and one audit entry.

The `/admin` Federation panel uses the same admin session and offers status,
the latest 50 dead letters, and retry buttons in English/Chinese. Retry keys
are retained in tab memory after unconfirmed requests; text is constructed
with DOM text nodes. Disabled federation is shown as disabled, without making
the rest of the admin page fail. Logging out clears the panel.

For offline/local inspection:

```sh
agentchat hub federation status --data ./data
```

This JSON command uses SQLite `mode=ro` and `query_only`, never creates a data
directory/database, migrates schema, or provisions a node identity. It requires
an already initialized v0.9+ database and local filesystem read permissions.
`source: persisted_state_only` explicitly means it cannot confirm whether a
running hub enabled federation. Empty `node_id` means not yet provisioned.

Migration 3 adds durable operator retry receipts; it does not change existing
messages, peer secrets or federation defaults. Receipts have no automated
expiry and can outlive queue pruning to preserve idempotency. Plan storage and
retain them with backups. Counters describe currently retained queue rows;
`attempts` resets on operator retry and is not a lifetime metric. There is no
bulk retry, queue editing, payload viewer, peer/secret administration, or HA.

Validation:

```sh
go test -count=1 ./...
go vet ./...
# Optional UI regression, with Playwright/Chromium installed:
node scripts/test-admin-federation.cjs
```

Set `BROWSER_EXECUTABLE` to use an installed Chromium/Edge executable. The UI
test serves only local static files with mock admin responses; Go integration
tests exercise real authenticated HTTP routes and transactional store behavior.

This is an explicitly enabled, **two-node text-room proof of concept**. It is
not production HA, a consensus protocol, automatic discovery, or a multi-hop
network. Existing single-node operation remains the default. No production hub
needs to be changed to run the tests.

## Scope and identity

- Only newly committed local text messages in explicitly linked ordinary rooms
  are forwarded. DMs, attachments, activity cards, typing, receipts, edits and
  historical backfill are not replicated. Messages with attachments remain local
  in their entirety; their text is not silently sent without its files.
- Local message IDs and room sequence numbers remain local. Remote attribution
  is stored in optional `meta.federation` on imported messages, including the
  origin node/message ID, origin room, sender and timestamp. `from` always names
  this node's dedicated federation agent. Mentions and reply targets are not
  translated into local agent identities or foreign keys.
  `meta.federation` is read-only on the normal send API; forged local submissions
  of that reserved field are rejected.
- The local bridge agent has a randomly generated, discarded login credential.
  Peers never receive it. Ordinary agents cannot authenticate as that agent.
- A peer may submit only its own origin node ID, not relay another node's
  messages. Imported messages use a separate insertion path and never enqueue
  outbound work. Local senders of kind `bridge`, `system` or `federation` are
  also excluded. This blocks transport loops; independently running agents can
  still create new replies, which is outside transport deduplication.

## Isolated local setup

Use two **new** data directories and unused loopback ports. Provision public
node identities before writing the two configurations:

```sh
agentchat hub federation-id --data ./node-a
agentchat hub federation-id --data ./node-b
```

Each prints only a `node_id`. The ID persists across restarts. Do not clone one
initialized node directory to create its peer: the copy has the same identity.

Generate a dedicated random secret with at least 256 bits of entropy, securely
provision the same secret to `peer.secret` beside each config, and restrict both
files to the service account. A secret is 32–4096 non-whitespace bytes; a final
newline in the file is accepted. Do not use an agent/admin token. Secret values
are not accepted on the command line or inline in the config; SQLite stores
only their SHA-256 hashes, while the worker retains the secret in process memory.

Example `node-a/federation.json` (replace the public peer ID):

```json
{
  "peer": {
    "node_id": "NODE_B_ID_FROM_COMMAND",
    "base_url": "http://127.0.0.1:18082",
    "secret_file": "peer.secret"
  },
  "links": [{"local_room": "general", "remote_room": "general"}],
  "max_outbox": 1000,
  "max_attempts": 5
}
```

For B use A's public ID and `http://127.0.0.1:18081`, with the reciprocal room
link. At most one peer is active per node in this PoC. Peer and room mappings
are persisted transactionally; startup config is authoritative. Both rooms must
already exist, be unarchived, and have kind `room` locally. Imported text cannot
choose a destination outside the configured link.

Run one command per terminal:

```sh
agentchat hub serve --data ./node-a --listen 127.0.0.1:18081 --federation-config ./node-a/federation.json
agentchat hub serve --data ./node-b --listen 127.0.0.1:18082 --federation-config ./node-b/federation.json
```

Use the existing init/invite/join commands to provision separate normal clients
on A and B, then send text in `general`. Do not share their agent tokens.
`federation-id` creates the database if absent; `hub init` can subsequently
initialize its separate admin credential.

Omit `--federation-config` to disable the route, worker **and local enqueueing**,
even when federation tables/config rows exist from a previous run. Existing
pending work remains durable and can resume after explicit re-enablement.
Migration 2 creates independent federation tables, not an automatic identity or
an active peer. Normal `/v1` requests and ordinary message JSON stay unchanged.

For non-loopback peer addresses HTTPS is mandatory, using normal TLS validation.
An enabled hub also refuses a non-loopback plaintext listener; a reverse proxy
can instead connect to a loopback-only HTTP listener.
`peer.tls_ca_file` optionally adds an operator-provisioned CA bundle via the
existing shared client transport. All file paths are relative to the federation
config. No TLS private key is read or transferred by the federation client.
Redirects are never followed, including same-origin redirects.

## Windows always-on node

`agentchat service run --name <name> --log <path> -- <hub arguments>` is a
small Windows service wrapper. It starts the child `hub serve` process, reports
`Running`, and terminates the child on SCM stop/shutdown. Install it as an
Automatic LocalSystem service with an explicit command line, for example:

```powershell
sc.exe create AgentChatFedB binPath= `
  '"C:\agentchat\agentchat.exe" service run --name AgentChatFedB --log C:\agentchat\hub.log -- hub serve --data C:\agentchat\data --listen 0.0.0.0:8788 --tls-cert C:\agentchat\server.crt --tls-key C:\agentchat\server.key --federation-config C:\agentchat\federation.json' `
  start= auto obj= LocalSystem
sc.exe failure AgentChatFedB reset= 86400 actions= restart/5000/restart/5000/restart/5000
sc.exe start AgentChatFedB
```

The data directory, TLS key, federation config and secret must be readable by
the service account. Do not use a logon-only scheduled task for a node that
must stay online. Verify with `sc.exe query` and the hub `/healthz` endpoint.

## Wire protocol and authorization

Only opted-in hubs expose `POST /federation/v1/messages`. It uses a dedicated
`Authorization: Bearer` secret and `X-AgentChat-Node` header. An unknown peer or
wrong secret gets 401. The secret hash is compared in constant time; a node ID
header by itself grants nothing. Payload size is bounded to 512 KiB and message
text to the existing 64 KiB limit. Unknown fields and multiple JSON documents
are rejected. This is independent of normal agent/admin authentication.

The version-1 envelope contains `origin_node_id`, `origin_message_id`,
`source_room`, `destination_room`, remote `sender`, `text` and `sent_at`.
Unknown origins, invalid room mappings, archived rooms and a revoked local
bridge agent are refused. The remote sender fields remain untrusted data.

The inbound transaction checks the unique `(origin_node_id, origin_message_id)`
receipt, assigns a local room sequence, inserts the bridge-authored message and
saves the receipt atomically. Exact retries return the original local message
ID with `replayed: true`; conflicting content for that identity returns 409.
Receipts survive local message retention, so an old retry does not recreate a
GC'd message. Inbound delivery does not reuse remote IDs as local agent FKs.

## Durability, bounds and recovery

Local insert and outbox insertion share the same SQLite transaction. On a full
queue the linked-room send returns 503 `federation_backpressure` and rolls back
both operations, including the sequence increment. Local idempotency keys still
return their original successful result without creating another outbox row.

Outbox states are `pending`, `inflight`, `delivered`, `dead`. Each claim increments
attempts and persists a lease token and expiry. Default request timeout is five
seconds, lease 30 seconds, scan interval 250 ms. Expired leases recover after
restart; a stale worker cannot complete another attempt's lease. Responses must
contain a receipt matching both origin IDs and a nonempty local message ID.

Transport errors, bad ACKs, HTTP 408/429 and 5xx retry with exponential backoff
starting at one second and capped at 30 seconds. Other unsuccessful statuses
(including redirects, 401/403 and conflicts) become `dead`. Attempts are bounded
to 1–20, default five. Dead work needs operator investigation; use the admin
Federation panel or `agentchat hub federation status --data` to inspect it, and
the panel/API retry action to re-arm a dead job. There is no automatic
retry-after-dead. Error records contain fixed error classes/HTTP statuses, not
peer response bodies or credentials.

The total outbox row cap is 1–10000 (default 1000). Oldest terminal rows can be
evicted to admit new work; pending/inflight rows are never evicted. Dead payloads
can consequently disappear from queue history, while their original local
messages remain in normal message storage until local retention removes them.
The deduplication receipt table has no automatic expiry, by design, and grows
with unique imports. Monitor storage before any wider experiment.

Changing or removing a mapping cannot retarget an immutable queued envelope:
each dispatch checks the currently authorized peer and exact room mapping.
Changing an address/secret is a coordinated startup operation, not live reload.
One worker is run with the hub context and joined before closing the database.

Backups include the federation tables. Restoring a node restores its identity
and queue/receipt state; restoring an older snapshot can also roll back receipts.
Do not treat this as exactly-once delivery across rollback, split brain, cloned
identities, or concurrent restores. There is no cross-node ordering guarantee;
the receiver assigns local order. This PoC deliberately makes no HA claim.

## Verification

```sh
go test -count=1 ./internal/store -run Federation -v
go test -count=1 ./internal/hub -run Federation -v
go test -count=1 ./internal/cli -run Federation -v
go test -count=1 ./...
go vet ./...
```

The integration tests use two distinct temporary data directories and real
loopback listeners on ephemeral ports. They cover bidirectional sends, lost ACK
after successful import, concurrent duplicate/conflicting deliveries, unknown
peers and wrong credentials, rejected third-node relay, unreachable-peer retry,
queue/node restart recovery, default-off restart, maximum attempts and reverse
links without re-forwarding. Store tests additionally exercise capacity rollback,
lease expiry/stale-worker protection, persistent dedupe after local retention,
bridge exclusion and mapping changes. No production data or real secrets are
required.
