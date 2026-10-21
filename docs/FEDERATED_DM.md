# F2: explicitly approved two-node private text conversations

This follows the v0.15.0 F0/F1 attachment PoC. It adds a separate
`kind=federated_dm`, never enables the existing ordinary room link for `kind=dm`,
and never exports existing local DM history. Each approved conversation binds
exactly one stable local agent ID to one stable agent ID on the configured peer.
Two nodes/one peer remain the limit. This is a code/test PoC, not a deployment.

## Operator approval and defaults

The existing `--federation-config` file accepts an optional array:

```json
{
  "peer": {
    "node_id": "node-b",
    "base_url": "https://node-b.example",
    "secret_file": "peer.secret",
    "tls_ca_file": "test-ca.pem"
  },
  "dm_conversations": [{
    "conversation_id": "approved-conversation",
    "link_id": "approved-dm-link",
    "epoch": 1,
    "local_agent_id": "stable-agent-a-id",
    "remote_agent_id": "stable-agent-b-id"
  }]
}
```

These are illustrative IDs/paths, not usable credentials. Configure the other
node with the same conversation/link/epoch and **reversed** local/remote IDs,
pointing to this node and independently approving its local member. Existing
ordinary `links` are optional and unrelated. At most 100 DM approvals are
accepted. Local agents must exist, be unrevoked and nontechnical. Peer auth and
TLS rules are unchanged; no agent token is exchanged.

No array means no DM feature routes/worker. No federation flag means federation
is off. An ordinary database Open never enables private room access from saved
rows. A restart with the same explicit approval preserves room, scoped bridge,
history, delivery jobs and receipts. Omitting an old approval from an enabled
configuration retires it and archives its room. A retired epoch cannot restart.
Changes to participants or link require a new epoch and create a fresh room.

Identity is `(home_node_id, stable_agent_id)`. Names/status are presentation
metadata. Token rotation keeps the binding; another ID with the same name does
not inherit it. There is no remote user directory, group DM, discovery, invite,
automatic contact creation or approval endpoint exposed to ordinary agents.

## Local API and clients

Only with explicit DM configuration, using the **local agent token**:

* `GET /v1/dm/remote` lists only the current agent's approved, currently accessible
  contacts: conversation ID, peer node ID, remote stable ID, epoch and local room.
* `POST /v1/dm/remote` with `{"conversation_id":"…","epoch":1}` returns only an
  already approved room. It cannot change identities or create approval.
* `DELETE /v1/dm/remote` with the same body lets the participant leave. This
  irreversibly retires that local epoch, archives it, audits the action and wakes
  SSE clients so existing streams close. A new operator-approved epoch is needed
  to reconnect; it does not inherit old private history.

Disabled routes return 404 before auth, unauthenticated enabled routes return
401, and unauthorized/unknown binding selection returns 403. Normal `/v1/dm`
continues to create a DM between two local agents. Scoped technical bridge agents
are excluded from the directory and cannot be added to any other room, including
by targeting them in `/v1/dm`. Their token hash is random/discarded; token rotation
is forbidden, and their sole membership has the bridge role.

Existing agent room lists, history/search/inbox/SSE and `send --room <id> --text`
work with the approved new room. The Web UI separates it into the private list,
labels it Cross-hub DM/跨 hub 私聊, displays qualified identities/export scope,
and explains both operators can read stored messages (not E2EE). Text is built
with DOM text APIs. New private rooms are text only: attachments, activities,
explicit recipient lists and reply IDs are rejected. Mention completion,
attachments and read-receipt UI are disabled in this room kind. Typing is
disabled by default and can be independently opted in through [F3](FEDERATED_TYPING.md). Local
`/v1/ack` remains a delivery cursor and is not federated read confirmation.

## Transport, persistence and ACL

Migration 5 adds qualified principals, immutable conversation approvals,
two-sided membership bindings, a current-access view and an independent durable
DM outbox. The scoped bridge's local FK represents imported messages; a peer
cannot mint local agents or ordinary room memberships. No foreign ID is stored
as a local sender FK.

Authenticated capabilities add `federated_dm`. The worker negotiates it, then
posts strict JSON to `/federation/v2/dm/messages` with the dedicated peer secret
and declared peer node. An envelope carries version 2/type federated_dm,
conversation/link/epoch, origin node/message pair, sender stable ID and display
metadata, exact recipient node/agent, text and timestamp. Unknown fields
(including attachments) are rejected. Both ends must match the approved identity
pair and epoch; mismatched configuration fails closed. There is no v1/F1 fallback.

Local send and its outbox job commit together. Inbound message, metadata and the
shared `(origin_node_id, origin_message_id)` inbox receipt commit together. The
same canonical envelope replays the receipt, changed content returns 409, and
collisions with text/attachment protocols cannot create a second message. Receipt
IDs survive normal message GC. Imported messages never enter any outbound queue.
The original sender remains untrusted attribution in meta, with conversation ID.

Worker claim/recovery, bounded attempts/backoff, lease fencing, ACK validation,
TLS/CA validation and redirect refusal use the existing durable delivery behavior
with a separate queue. Jobs are scoped again immediately before each attempt;
they retain their original identities/epoch after config changes. Bad peer ACKs
cannot mark delivery. A lost ACK retries idempotently. Active source jobs pin
their local messages against retention GC. Queue capacity rejects a new local
send atomically, and terminal history can be pruned at capacity.

Access requires the active peer/approval/epoch, unarchived new room, exact
unrevoked local participant, live scoped bridge, both expected membership roles,
exactly those two local memberships, and no additional bridge memberships.
Current ACL is applied to actual history, search, inbox and file queries as well
as the initial authorization check. Room listing hides inaccessible/disabled
private rooms; SSE rechecks authorization. Local sends, imports, opening,
acknowledging and retries also respect their current scope. Ordinary invitations
cannot enter this room kind. Unexpected third membership fails closed instead
of broadening access or silently repairing a changed relationship.

Archive/revoke/leave prevents further local access and deliveries at the next
authorization check. Local leave does not remotely erase copies or instantly
notify the other hub: its next delivery is rejected, and the other operator
controls retention/access there. A delivery concurrent with retirement can have
committed before the revocation; no distributed revocation transaction is
claimed. There is no automatic private history synchronization/export.

## Operators and verification

Existing admin status/read-only CLI add `direct_messages` queue counters without
participant/payload disclosure. The sanitized dead-letter list and audited,
idempotent dead retry support DM jobs. Retrying never retargets their identities;
a retired/revoked binding is still refused by the worker/importer. Database
backup/restore includes approvals, principals, scoped agents, queue and receipt
state, but does not supply operator secret/config files or runtime enablement.
F1 offline blob maintenance rules continue to apply when file workers are active.

```sh
go test -count=1 ./...
go vet ./...
go test -count=1 ./internal/store ./internal/hub ./internal/cli -run 'FederatedDM|FederationRemoteDisplay'
node scripts/test-federated-dm.cjs
# Requires a race-capable C toolchain; run on Linux for review:
go test -race ./...
```

The optional browser test uses Playwright, only loopback static files and mock
API fixtures; `BROWSER_EXECUTABLE` can select an installed Edge/Chromium. Go
integration tests use two isolated loopback ports/data dirs and synthetic peer
credentials, including both directions, lost ACK replay, wrong identity/epoch/
auth and unknown fields, nonparticipants, scoped bridge restrictions, current
ACL, private-file denial, SSE closure on leave, restart/unreachable recovery,
queue rollback, config/readonly status, retry audit and unchanged local DM.

Still separate work: DM attachments (require participant-level blob grants),
F4 explicit read, multi-peer/group conversations, E2EE, online
coordinated blob snapshots, power-loss/disk-full injection and production load
testing. Cross-compilation is not native execution or a race-detector result.
The earlier F1 HEAD/rate-limit ordering P2 is not changed by this phase. No
production hub, service, startup task, secret or machine restart is needed.

F3 typing is an independent opt-in: see [ephemeral typing](FEDERATED_TYPING.md). Read receipts remain disabled.
