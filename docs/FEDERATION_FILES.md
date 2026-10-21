# F0/F1: opt-in two-node attachment PoC

Based on v0.14.1 (`6331682`). This phase supports two nodes, one independently
authenticated peer, and explicit links between ordinary rooms. It does not add
DM, typing, read receipts, multi-hop, HA, or desktop platform changes. Existing
v1 text envelopes and the local 512 MiB upload limit remain unchanged. Without
`--federation-config`, federation remains off on every ordinary restart.

## Enable only on isolated test nodes

Provision each node and the existing peer secret file as in FEDERATION.md. Add
these fields to a room link on **both** nodes, reversing local/remote rooms:

```json
{
  "local_room": "general",
  "remote_room": "general",
  "attachments": true,
  "link_id": "operator-provisioned-link",
  "epoch": 1
}
```

`attachments` defaults to false. A common link ID and positive epoch are
required. A peer/link/epoch mapping is immutable; omitted epochs retire and
cannot be reused. Changing room mappings requires a fresh epoch. Only active,
unarchived `kind=room` links work. Retiring a link revokes its blob grants and
prevents staged messages from being committed. Old queued payloads keep their
original protocol, mapping and epoch; they are never recoded or downgraded.

HTTPS with the configured CA is required outside literal loopback. The blob
client uses the same TLS validation, fixed configured peer origin and redirect
refusal as federation text. No manifest URL or filesystem path is accepted.
Agent/admin tokens are never shared with the peer or accepted as peer auth.

## Wire protocol and delivery

All routes require the dedicated peer Bearer secret and `X-AgentChat-Node`:

* `GET /federation/v2/capabilities`: version, attachment feature and fixed limits.
  Available with explicit federation, reporting false if file links are absent.
* `POST /federation/v2/messages`: strict, single bounded JSON document, version 2,
  origin pair, source/destination rooms, link ID/epoch, original sender metadata,
  timestamp, caption, and 1–8 file manifests. Each manifest contains only SHA-256,
  exact size, display name and a MIME hint. Repeated hashes are rejected.
* `HEAD/GET /federation/v2/blobs/{sha}`: query binds `origin_node_id`,
  `origin_message_id`, `link_id`, `epoch`, and transport `generation` (default 1).
  GET requires one explicit bounded byte range and a strong SHA ETag in If-Range.
  HEAD reports size/ETag. Unknown files and invalid grants both return 404 after
  peer authentication. There is no generic hash lookup or file-ID export.

File routes return 404 if attachment links are absent; capabilities also returns
404 if federation itself is disabled. V1 accepts its original schema only.

A local attachment send commits message, v2 outbox, exact blob grants and pins in
one SQL transaction. Queue pressure or an invalid linked manifest rolls back the
local send. Attachments never enter the v1 text queue. No attachment failure can
silently send only the caption.

Inbound 202 means **durably staged**, with a transfer ID and no local message ID.
The sender persists waiting_receipt and polls by reposting the same canonical
envelope; healthy 202 responses do not consume its failure budget. Only a 200
`committed` receipt with the exact origin pair and nonempty local message ID
marks delivery. Unsupported peers cannot trigger a v1 fallback.

Each file is pulled, fsynced, checked for exact length/full SHA, and atomically
installed before its file row and staging pin commit. The complete manifest is
rechecked before publication. Then one SQL transaction creates the message,
attachments, global inbox dedupe record and final receipt. SSE notification
follows commit. Original attribution lives in meta; sender FK is the local
non-login federation agent. Imported/bridge/system/activity messages do not
forward, preventing two-node loops. Empty captions with attachments are allowed.

The origin pair is unique across both protocol versions. Exact duplicate staged
or committed deliveries return the same receipt; changed canonical contents
return 409. Final receipts survive normal message retention. A dead receiver
transfer returns 410 until a newer explicitly audited transport generation is
submitted before the original deadline. Generation is excluded from semantic
hashes. An older generation cannot revive pending work or extend its deadline;
committed content always replays its final receipt.

## Bounds, recovery and storage

* 32 MiB/file, 64 MiB/message, 8 files; 512 MiB total receive accounting.
* At most 100 pending/inflight manifests per peer, two download workers, two
  source blob requests, 4 MiB ranges, and a fixed 24-hour deadline per transfer.
* Staging admission atomically reserves twice the manifest size to cover the
  saved prefix plus a new lease's copy. Verified provenance is charged as files
  become ready; spare reservation is released at commit. Physical hash dedupe
  retains one blob. Concurrent staged duplicates may conservatively reserve more
  than actual bytes, so 429 can occur below 512 MiB of visible attachments.
* Every manifest is actually fetched from that peer, even if an unrelated local
  upload has the same hash. Only a completed, verified transfer establishes
  `(peer, hash, size)` provenance. Physical dedupe is never proof of possession.
  Reusing the same peer's provenance to avoid network traffic is deferred.
* Temp basenames are generated internally; canonical blob paths derive only from
  validated lowercase SHA. Existing parents resolve within the real blob root;
  symlink/junction escapes fail closed. MIME hints never control imported file
  response types (new imported rows use application/octet-stream).
  Remote stored file names and attachment display names are normalized to a
  portable basename (both slash styles, controls removed, safe empty fallback).
  Original manifests remain unchanged for canonical replay/conflict detection.
* A checkpoint is persisted only after fsync. Restart copies the checkpointed
  prefix into a different lease's file, then resumes exact Range/ETag/size
  responses. A bounded 200 restarts from zero. Invalid ranges/encoding, extra or
  short bytes, and hash mismatch fail closed. A corrupt prefix is detected by
  the final full hash and reset. Missing checkpoints restart from zero.
* Persisted lease tokens fence late workers. Transport failures use bounded
  exponential backoff and the configured attempt budget; crashes preserve the
  lease until recovery. Deadlines cannot be renewed by ordinary duplicate POSTs
  or operator retry. Terminal source rows expire after 24 hours and can be
  reclaimed on a later send; a full retained queue rejects new linked sends.

Migration 4 adds independent file-links, file-outbox, precise-grants, transfers,
transfer-pins and received-provenance tables. It leaves the original outbox
state constraint and v1 wire schema intact. Existing databases migrate forward;
no runtime attachment enablement is inferred from persisted tables.

## Operators and maintenance

Existing admin status adds an `attachments` object: pending, inflight,
waiting_receipt, delivered, dead, attempts, staged and reserved_bytes (including
verified received blobs). Existing sanitized outbox listing includes both
protocols and accepts waiting_receipt. The existing dead-letter retry endpoint
also handles file jobs, atomically increments transport generation, records its
idempotency receipt and audit, and preserves origin, payload and deadline.
Expired file jobs return 409 expired. No manifests, payloads, grants, secrets,
hashes of credentials, temporary paths or leases are exposed by operator DTOs.
The read-only CLI reports the same persisted attachment counts:

```sh
agentchat hub federation status --data ./node-a
```

This PoC permits one attachment runtime per real blob root, guarded by an OS
lock that releases on process death. **Stop the isolated hub before backup,
restore or GC.** These commands acquire the same guard and refuse while the
attachment worker is active; this phase does not implement online coordinated
blob snapshots. Prevent any other process from writing the stopped data dir.

Backup includes actual content-addressed and staging files with SQLite state;
backup and restore check referenced federation blob sizes/full hashes and FKs.
A restored incomplete checkpoint can restart safely, and active leases expire
normally. Restore also refuses the active runtime's root. GC pins source files
and messages needed for current delivery, and installed unpublished files. On
`--apply`, it cleans expired uncommitted staging, retires terminal/expired pins,
then removes ordinary unreferenced file rows/blobs. Provenance cascades with file
deletion so quota is reclaimed. Committed dedupe receipts are preserved. Dry-run
does not sweep or release reservations. GC does not delete committed received
attachments while their ordinary message references remain.

## Validation and remaining boundaries

```sh
go test -count=1 ./...
go vet ./...
go test -count=1 ./internal/store ./internal/hub ./internal/cli -run FederationFiles
# On a host with a Go race-capable C toolchain:
go test -race ./...
```

Go integration tests create two loopback ports, separate data dirs and synthetic
secrets. Coverage includes both directions, captionless multi-file messages,
visibility atomicity, exact replay/conflicts across versions, peer/grant/epoch
auth, strict input, missing/corrupt files without text fallback, Range recovery
after reopening SQLite, stale leases, concurrent quota and staging limits,
audited retry generations, pin-aware GC, locked maintenance and backup/restore.

This is a test-node PoC, not a production/HA release. OS crash/power-loss and
disk-full fault injection, full throughput testing, live coordinated snapshots,
network cache reuse and automatic at-rest corruption repair remain open. Staged
dead work retains its bounded reservation/pins until retry or offline GC after
24 hours; run retention/GC deliberately to reclaim received storage. Windows
tests do not establish Linux/macOS runtime behavior or race-detector results.
F2 federated_dm, F3 ephemeral typing, F4 explicit read and desktop platform work
remain separate reviews after this phase is merged. No production or secret
configuration changes are part of this implementation.
