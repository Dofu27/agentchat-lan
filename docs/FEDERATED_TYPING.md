# F3: opt-in ephemeral typing (two nodes, one peer)

This experimental feature sends typing presence only. It does not implement
F4 read receipts, delivery acknowledgements, message history, multi-hop or HA.
Federation remains disabled without `--federation-config`; typing additionally
requires `typing: true` on each approved link at **both** ends. No migration or
new persistent table is needed. Restarting without that flag hides the entire
`/federation/v2/ephemeral` namespace with 404, even if other federation features
are active. Nothing in this change deploys or modifies a running hub.

## Explicit configuration

Ordinary room links add a shared identity/epoch and typing opt-in independently
of attachment opt-in. Example link entry (inside the existing config):

```json
{"local_room":"general","remote_room":"general","link_id":"general-link","epoch":1,"typing":true}
```

For an already approved F2 `dm_conversations` entry, add `"typing": true`.
The existing conversation ID, link ID, epoch and fixed local/remote agent pair
are mandatory. This does not enable ordinary `kind=dm` federation, DM attachments
or read receipts. Existing local typing for ordinary rooms continues to work
without federation. The room-list response adds `ephemeral_typing: true` only
for currently authorized opted-in scopes; the Web UI uses it to enable F2 typing.
Disable by omitting typing or setting it false on both ends and restarting the
experimental runtime. Identities and secrets remain in existing operator config,
never in event payloads or the UI. Outside literal loopback, peer HTTPS and the
existing CA verification are required; redirects remain refused.

## Independent wire protocol

Both POST routes require the separately provisioned peer Bearer secret and
`X-AgentChat-Node` matching the single configured peer. Agent/admin tokens do
not authorize them. JSON is strict, single-object and limited to 8 KiB.

* `/federation/v2/ephemeral/session`: `{boot_id, scope}`. Boot IDs are random
  per runtime. A room scope contains `kind:"room"`, `link_id`, `epoch`,
  `source_room`, `destination_room`. An F2 scope instead contains
  `kind:"federated_dm"`, `conversation_id`, `link_id`, `epoch`,
  `source_agent_id`, `destination_agent_id`. The receiver checks the exact
  reversed configured scope and its current store authorization. It issues
  `{session_id,ttl_ms:5000}`. The receiver-issued opaque capability is bound to
  that scope and peer, has a fixed lifetime, and cannot be renewed/replaced
  while active (409). It is never exposed to browser SSE or persisted.
* `/federation/v2/ephemeral/events`:
  `{session_id,seq,actor:{id,name,kind},typing,ttl_ms}`. Sequence numbers must
  strictly increase across the session; zero/replayed sequences or old/expired
  sessions return 409. TTL must be 1–5000 milliseconds and is capped again at
  the receiver's session deadline. Invalid fields return 400, unauthorized
  scope/DM actor 403, bounded capacity/rate excess 429. Room actor attribution
  is asserted by the authenticated home peer; DM actors must match the exact
  approved remote participant. Local technical bridge/system agents cannot
  originate outbound typing. Current link/bridge/member authorization is
  rechecked before sending, applying and displaying state.

SSE keeps its existing `agents` array for local names and adds `remote_agents`:
`[{node_id,agent_id,name,kind,ttl_ms,revision}]`. The revision distinguishes
real refreshes from unrelated SSE wakeups without extending TTL. Text labels
show `name@node · remote` (localized); DOM construction uses text content.
Remote names matching the local user's name still appear with their origin.
Browser timers remove expired entries without waiting for a heartbeat. Room
switch, logout and browser stream disconnect clear remote display state.

## Bounds, loss and lifecycle

All session, sequence, pending and remote state is RAM only. Maximum 64 opted-in
scopes, 64 remote actor entries per link, 256 across links, and 256 pending local
actor keys. Inactive entries briefly serve as rate-limit tombstones, then expire.
The sender keeps only the latest local state per actor/link, spaces it by at
least 200 ms, and services at most one key per 50 ms tick across the peer. The
receiver coalesces actor updates in 200 ms windows and uses fixed peer/link
rate buckets (20 events/second with small bursts). Session creation has its own
bounded peer bucket. Excess is dropped or rejected; there is no durable queue.

Source age is deducted from TTL, including handshake time. Request timeouts
are at most one second. A failed request discards pending state and sessions,
including incoming display state; new local input is required to reconnect.
No background retry, offline catch-up, expiry refresh or outbox exists. Session
creation and event delivery failures can lose typing updates. A receiver may
keep its fixed session until its deadline after a lost response or sender
restart, so a new session can be refused for up to five seconds. This is an
intentional best-effort gap; do not infer connection health or message delivery
from typing. Idle disconnections are represented by TTL expiry (no persistent
connection/heartbeat); runtime shutdown/restart clears local RAM immediately.
Events accepted from a peer only update local SSE and are never forwarded.

Ephemeral routes never write messages, audit rows, persistent tables or the
message/file/DM outboxes. Typing/SSE authentication checks revocation read-only
instead of refreshing `last_seen` on every keystroke/wakeup. Consequently an
otherwise idle SSE-only user does not refresh presence through that stream;
ordinary authenticated API operations retain their existing refresh behavior.
Request logs contain only the fixed endpoint path/status, not actor/session
payloads. Existing message SSE still checks revocation after its history read.

## Isolated validation

```sh
go test ./internal/hub -run FederatedTyping -count=1
go test ./internal/cli -run FederatedTyping -count=1
go test ./...
go vet ./...
node scripts/test-federated-typing.cjs
```

The Go tests run real HTTP/SSE on two loopback ports and separate temporary
databases, using synthetic peer credentials. They cover bidirectional room and
F2 typing, no second forwarding, whole-database equality, default-off 404,
wrong credentials/scope/actor, sequence/session/TTL rejection, restart clearing,
actor capacity/coalescing and offline input loss without replay. Browser tests
use loopback assets and synthetic APIs only. Run `go test -race ./...` on a
CGO/race-capable host before merging; native Windows race is not validated by
the non-CGO environment used for initial implementation. No production or
real-secret testing is required or performed.
