# Producer-owned structured logs and read-only broker contract

The hub, agent loop and codex bridge write files themselves. The UI is only a
viewer: closing it, killing it or disconnecting its SSE stream does not stop
collection. This change provides the backend contract, not a desktop panel,
iframe, UI-owned hub or UI-owned child stdout collector. No deployment is needed
to exercise the isolated tests.

## Producers and file format

`hub serve`, `agent` and `codex-bridge` accept:

* `--log-dir`: hub default `<data>/logs`; bridge default `logs` beside its config.
  Explicit absolute paths are recommended for foreground use and required for
  the Windows service wrapper. Do not share a directory with configs or secrets.
* `--log-max-bytes`: 64 KiB–64 MiB per newly written segment, default 10 MiB.
* `--log-files`: 2–32 retained segments **per component**, default 5, including
  the active segment. Rotation deletes the oldest segment beyond this count.
* `--log-body-summary`: default false. Explicit opt-in records only character
  and line counts, never a text preview. Arbitrary strings cannot be reliably
  scrubbed of unknown credentials, so this initial contract offers no raw body
  mode. Message bodies, attachment bytes, private keys and child command output
  have no representation in the log schema.

Normal CLI business stdout (for example an explicit message-print command) is
unchanged for compatibility; it is not a logging source and is never tailed by
this broker. Generic stderr/exit status remains available if files cannot open.

The fixed components are `hub`, `bridge` (loop and codex hook together), and
`service` (Windows supervisor metadata). Files are UTF-8 JSONL:
`hub-00000000000000000001.jsonl`, then segment 2, etc. Each append is one
complete record followed by LF, without a BOM. Example:

```json
{"v":1,"time":"2026-10-21T04:00:00Z","level":"info","component":"hub","event":"request","request_id":"01EXAMPLE","room":"general","agent":"agt_01ARZ3NDEKTSV4RRFFQ69G5FAV","route":"POST /v1/rooms/{room}/messages","status":201,"duration_ms":3}
```

`level` is debug/info/warn/error. `event` is a fixed enum: producer started/stopped,
HTTP request/internal_error, inbox_failed/received, hook_started/hook_done/
hook_failed/ack_failed, queue_started/queue_pending/queue_confirmed/queue_failed,
state_failed/app_start/app_failed, and service_started/service_stopped/child_failed.
Fields not applicable to an event are omitted. `request_id` identifies a hub
HTTP request; bridge events correlate by room/agent/seq. Default metadata does
not include display names, raw errors, executable arguments, URLs, query strings,
headers, token hashes, full messages or attachments. Request routes are declared
mux templates from a fixed allowlist, never the received URL. IDs have bounded
syntax and token-like values are redacted; public agent ULIDs are preserved.
New routes must be added to `internal/logbroker/routes.go` to appear in logs.

Each append takes a bounded OS lock shared across processes, reopens the current
segment, repairs a partial last append if necessary, writes and syncs the line,
closes the file, then enforces retention. Rotation creates a higher numbered
segment instead of renaming an open file. Process death releases the lock. The
writer does not retain handles that a viewer must own or pump. Readers accept
complete valid lines only, discard unknown fields, and reapply the schema and
redaction rules. A partial final line is not returned or advanced past.

Use the **same rotation settings for all writers of a shared directory**. An
operator lowering the segment size must account for already retained older,
larger segments; they disappear through normal count-based retention. Three
components at defaults retain at most 15 normal 10 MiB segments, plus a small
lock file. The active directory must remain a dedicated trusted local directory;
do not place it on a network share. Symlink/junction roots, linked parent paths
and nonregular segment files are refused. The log is an operational record, not
a tamper-evident audit store: accounts allowed to write it can alter or remove it.

## Resume and filtering contract

A reader consumes records in segment/byte-offset order, oldest retained first.
A cursor is `v1:<component>:<20-digit-segment>:<next-line-byte-offset>`. Treat it
as opaque, save it only after handling the associated entry, and keep a separate
cursor for each component/filter view. Filters advance the cursor over skipped
records; changing a filter does not recover previously skipped records.

A batch contains:

```json
{"entries":[{"cursor":"v1:hub:00000000000000000001:201","record":{"v":1,"time":"2026-10-21T04:00:00Z","level":"info","component":"hub","event":"started"}}],"cursor":"v1:hub:00000000000000000001:201","gap":false,"more":false}
```

`cursor` also advances when all scanned rows were filtered out. `gap:true`
indicates retention removed the requested segment; display a visible warning and
continue from the oldest surviving segment. Do not imply a complete history.
Malformed cursors, offsets inside a line, another component's cursor and cursors
ahead of the stream are errors. Deleting/replacing the whole directory is an
operator reset and can invalidate cursors; explicitly start a new view then.
Each read returns at most 100 records and scans at most 256 lines/2 MiB. `more`
means the caller can request another batch; it is not a promise of delivery.
Limits bound individual reads even for highly selective filters.

## Hub administrator API

Enabled only when a log directory is configured (the production CLI enables it
by default). An unconfigured library server returns 404 for this namespace,
including unknown paths; it never falls back to the Web UI.

* `GET /v1/admin/logs?component=hub&level=error&after=<cursor>&limit=50`
  returns a batch. Component is fixed to hub; bridge/service logs are local-only.
  Level is an optional exact filter. Limit is 1–100 (default 50). Errors are 400
  for invalid query/cursor, 401 for non-admin credentials, 429 for rate limits and
  503 when log files are unavailable. All responses use `Cache-Control:no-store`.
* `GET /v1/admin/logs/events` has the same query. `after` takes precedence over
  `Last-Event-ID`. It streams `event:log`, whose JSON is one entry and whose SSE
  `id` is that entry's cursor, plus `event:cursor` with batch cursor/gap/more.
  Persist cursor events even when the level filter suppresses all entries.
  Reconnect using an Authorization header and the saved cursor; never put the
  admin token in a query. A retention gap arrives on the cursor event.

Both routes use the existing admin token checks and per-admin request rate
budget; agent and peer tokens cannot read them. SSE additionally caps concurrent
streams at 8 per hub, rechecks current admin credentials after every file read
and before emission, and polls at most once per second with a separate bounded
read budget. A stream closes after five minutes, token rotation/revocation,
read failure, rate exhaustion, disconnect or write failure. Write deadlines bound
slow viewers. Reconnect with the last processed cursor; the API stores no viewer
session or acknowledgement. Log-viewer requests do not generate watched request
logs, avoiding self-amplifying polling. Local/F3 typing routes are also excluded
from request logging to avoid a durable per-keystroke presence trace. No API accepts a path, mutates logs or
returns a raw line. TLS/admin security requirements remain unchanged.

## Local bridge broker and installation permissions

The local viewer uses the same read-only parser, without a hub/admin credential:

```sh
agentchat logs tail --dir /absolute/logs --component bridge --limit 50
agentchat logs tail --dir /absolute/logs --component bridge --after <cursor> --level error --follow
```

Output is one JSON batch per line. `--follow` polls files once per second and
handles OS interruption; the tail process may be restarted independently. It
does not run the bridge, collect its stdout, create a missing directory, migrate
the database, or change logs. Desktop integration can consume these batches or
embed the same reader behind its authenticated local interface. OS directory
permissions are the local access control; this is not an unauthenticated HTTP
server. Desktop capability/token integration remains a separate change.

At install time, choose one fixed directory, rotation policy and writer account:

```text
agentchat logs prepare --dir C:\ProgramData\AgentChat\logs --user-sid <intended-user-SID>
agentchat service install --config <absolute-config> --log-dir <absolute-logs> --log-max-bytes 10485760 --log-files 5
agentchat service run --name <SCM-name> --log-dir <absolute-logs> --log-max-bytes 10485760 --log-files 5 hub serve --data <absolute-data>
```

These are installation examples, not commands run by this implementation task.
`logs prepare` operates on a dedicated directory and recognized managed files
only; unrelated contents cause refusal. On Windows it grants protected DACL
access to LocalSystem, Administrators and exactly the designated user SID
(default current user). Existing managed files receive the same DACL; newly
created files inherit it. It grants no Everyone/Authenticated Users access.
When preparing as an administrator/LocalSystem, pass the intended desktop user
SID explicitly rather than relying on that process's current identity. Service
and named user can both write without making all local users writers/readers.
POSIX preparation uses directory0700/files0600: run service and user bridge as
the same account, or arrange a separately reviewed group policy externally.

`service install` records explicit log arguments in the launcher/user unit and
prepares permissions before activation. The existing command is a per-user
task/user-unit installer, not a new SCM installer. `service run` requires an
already existing absolute log directory; an SCM installer must prepare it first.
The wrapper and agent hook pass the fixed settings to children via
`AGENTCHAT_LOG_DIR`, `AGENTCHAT_LOG_MAX_BYTES`, `AGENTCHAT_LOG_FILES`, and
`AGENTCHAT_LOG_BODY_SUMMARY`. Explicit child flags still take precedence, so
installers must avoid conflicting overrides. No secret is placed in these values.

The Windows supervisor now records only lifecycle/failure metadata and discards
raw child stdout/stderr. Child hub/bridge processes own their own JSONL files.
Compatibility: legacy `service run --log <path>` selects its **directory** for
new structured files; it no longer appends to the old named plaintext file.
Existing plaintext logs are neither read, served, deleted nor migrated. Review
this behavior before an actual upgrade. No installed services were changed here.

## Failure behavior and validation

Failure to create/write structured logs fails startup. A runtime hub append
failure cancels the hub and joins its federation worker before closing storage;
bridge failures stop further processing/acknowledgement and return an error.
Errors are surfaced through generic stderr/exit status without dumping payloads.
No silent unbounded memory queue or stdout fallback exists. Slow disks/competing
writers can hit the one-second lock budget; per-record fsync and directory scans
trade throughput for independent durable collection. This needs workload testing
before production rollout. Filesystem exhaustion/power-loss is not simulated by
the initial tests; retention is not a backup or infinite replay guarantee.

Isolated validation:

```sh
go test ./internal/logbroker ./internal/hub ./internal/cli
go test ./...
go vet ./...
go test -race ./...  # CGO/race-capable review host
```

Tests cover restart while no viewer exists, saved cursor, retention gaps, partial
append recovery, multiprocess writers, bounds/filtering, redaction and body-summary
opt-in, read-only behavior, hub admin/agent/peer denial, live SSE, token rotation,
read/concurrent-stream limits, and synthetic Windows supervisor output suppression.
Windows ACL tests inspect permissions on temporary directories/files; they do not
launch a real LocalSystem service. Native SCM/account transitions, production
load, power-loss/disk-full and Linux race validation remain operator/review work.
