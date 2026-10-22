# Changelog

All notable changes to this project are documented here. The format follows
Keep a Changelog, and the project follows Semantic Versioning once it reaches
v1.0.

## [0.29.1] - 2026-10-22

### Fixed

- Windows console and installer embed the AgentChat icon; shortcuts use the
  executable icon, and the legacy Codex bridge loop no longer shows a cmd
  window.

## [0.29.0] - 2026-10-22

### Added

- Opt-in F4 federation read-receipt forwarding: authenticated, linked,
  monotonic remote cursor, additive `federation_receipts`, bounded best-effort
  fanout, no ACK changes and no re-forwarding loop
- Multi-peer federated DM and typing fanout: each configured peer has its own
  conversation, outbox, and ephemeral typing session; text DMs and typing no
  longer require a single-peer federation.
- Linux/macOS launcher rejects non-HTTP(S) or userinfo hub URLs, normalizes the
  URL, and shows role-specific guidance; it remains a browser launcher, not the
  full WebView console.
- Desktop Join saves a new agent config with `O_EXCL`; concurrent joins can no
  longer overwrite the first successful config.
- The self-contained Windows installer embeds the release version in its CLI
  (`agentchat v0.29.0`) instead of `dev`.
- Linux `.desktop` and macOS `.app` launcher packages, attached by the release
  workflow with checksums.
- English/Chinese console, chat, and admin screenshots in `docs/screenshots/`.

## [0.28.1] - 2026-10-22

### Fixed

- Installer checksum uses the release asset basename (not a local `dist/` path)

## [0.28.0] - 2026-10-22

### Fixed

- Release checksums now include the self-contained Windows installer; the
  workflow appends its SHA-256 and re-uploads `checksums.txt`

## [0.27.4] - 2026-10-22

### Security

- Desktop service start/restart now validates the actual SCM-registered
  executable, pins its ancestor chain through Start, and refuses unsafe legacy
  user-writable paths, identity changes or binPath drift before starting.
- Explicit resume now revalidates retained credential ownership and private
  permissions before reading it; Windows rejects unprotected/inherited or
  foreign-access DACLs and explicitly assigns new credentials to the initiator.
- Federation tests hard-assert 32 deliveries/32 attempts/zero dead jobs and
  verify expired claims cannot corrupt counters through stale completions.
- Hub-init durably writes the caller-generated admin credential before committing
  its DB hash; write/sync/close errors cannot initialize the DB. Failed attempts
  retain the credential without path-based cleanup. `--resume-init` explicitly
  resumes with that file, including an interrupted commit with the same token.
- Bridge/Host-init/service commands run under a Windows Job Object with
  `KILL_ON_JOB_CLOSE`; bounded output follows `io.Writer` and redacts
  credential-shaped values
- Job creation/configuration/process assignment/resume failures stop execution;
  injected failure tests ensure suspended children never run on those paths.
- Host service creation pins each ancestor and executable by handle, rejects
  reparse points, and checks ordinary-user effective mutation rights with
  AccessCheck before using the identity-bound executable for SCM installation.
- Output redaction covers complete Authorization/Authentication/Bearer values,
  JSON credentials, URL query secrets and private keys.
- Federation acquires the global delivery slot before claiming a lease

## [0.27.3] - 2026-10-21

### Changed

- Per-peer workers share an eight-slot global federation delivery budget
- Uninstall pre-validates the complete owned-file manifest before touching
  Startup/task/registry side effects

## [0.27.2] - 2026-10-21

### Added

- Installer tries an ONLOGON scheduled task when Startup/Run are blocked;
  uninstall removes the task. This host still denies all three, so the warning
  path is verified but real login startup remains untested here.

## [0.27.1] - 2026-10-21

### Fixed

- GoReleaser publishes a normal release (not a draft), so the workflow's
  installer upload finds it; retagged from a unique commit to avoid multiple
  tags on one commit confusing GoReleaser

## [0.26.6] - 2026-10-21

### Fixed

- Track `cmd/agentchat-desktop/launcher.html` (it was ignored by a broad
  `agentchat-desktop` ignore pattern, breaking Linux/macOS release builds)

## [0.26.5] - 2026-10-21

### Added

- `scripts/verify-reproducible.sh` builds the Linux CLI twice and compares
  bytes; the v0.26.4 tree passes

## [0.26.4] - 2026-10-21

### Security

- Bridge CLI execution now bounds stdout/stderr to 16 KiB and kills the whole
  child tree via `taskkill /T` on timeout/cancellation

## [0.26.3] - 2026-10-21

### Fixed

- Host init refuses an existing data directory/token file instead of relying on
  cleanup of paths it may not own
- Settings serializes with Host setup; uninstall aggregates startup/registry
  removal errors instead of returning only the first

## [0.26.2] - 2026-10-21

### Security

- Failed Host init no longer deletes data/token paths that another process may
  have created after preflight; it leaves them for operator inspection
- Uninstall distinguishes a missing HKCU Run value from query execution or
  permission failures and returns the latter

## [0.26.1] - 2026-10-21

### Security

- Uninstall keeps the verified install-root handle open through file removal
  (without `FILE_SHARE_DELETE`) so a junction cannot replace the verified root;
  normal uninstall and real-junction refusal were both re-tested

## [0.26.0] - 2026-10-21

### Changed

- Federation now runs one bounded claim worker per peer; a slow peer's backlog
  cannot consume all workers or starve another peer
- Host-init checks the desktop config generation before publishing and returns
  409 rather than overwriting a concurrent config update
- Status uses one client/origin/token snapshot for health and federation calls

## [0.25.0] - 2026-10-21

### Added

- Linux/macOS `agentchat-desktop` loopback launcher (bilingual page, browser
  open, no token exposure); GoReleaser archives for linux/darwin desktop

## [0.24.6] - 2026-10-21

### Fixed

- Host setup and Join serialize through a dedicated setup mutex; startup Run
  entry deletion is checked instead of silently ignored

## [0.24.5] - 2026-10-21

### Fixed

- Host-init failure rolls back only the data directory/token file created by the
  failed request
- Installer reports Start Menu shortcut failures; real-junction uninstall test
  confirms the identity guard refuses the junction and leaves the moved install
  intact

## [0.24.4] - 2026-10-21

### Fixed

- Bridge status/install/uninstall snapshots config under a short lock and runs
  the external CLI without holding the runtime lock

## [0.24.3] - 2026-10-21

### Fixed

- Status/outbox/retry/service use short runtime snapshots and no longer hold the
  runtime lock across hub HTTP, SCM, or request-body I/O

## [0.24.2] - 2026-10-21

### Fixed

- Host setup validates the TLS client before `hub init` and no longer holds the
  runtime write lock while running the CLI, reading the token, or saving config

## [0.24.1] - 2026-10-21

### Fixed

- Join no longer holds the runtime write lock during hub network I/O; it uses a
  short snapshot, performs the request outside the lock, then atomically
  persists and publishes `agent_config`

## [0.24.0] - 2026-10-21

### Security

- Chat token injection now requires the agent config `hub_url` to canonicalize
  to the desktop hub origin
- Installer ownership markers record volume/file identity; uninstall rejects
  reparse points, moved/junction roots, and pre-validates every manifest entry
  before deleting anything; a failed remove-data keeps the marker retryable
- Desktop config saves use a same-directory temp file, fsync, and replace
- Join/Host-init body parsing happens before the write lock

### Changed

- Multi-peer text workers run bounded concurrent consumers (up to four) so one
  slow peer no longer serializes delivery to another

## [0.23.3] - 2026-10-21

### Added

- Multi-peer federation attachment links use per-peer capability negotiation,
  blob clients, and authenticated peer selection; DM/typing remain single-peer

## [0.23.2] - 2026-10-21

### Tests

- Three-node multi-peer acceptance: fanout, offline-peer isolation, and refusal
  to forward a third-party message

## [0.23.1] - 2026-10-21

### Fixed

- Multi-peer config loading no longer rejects the normal file directory passed
  by `hub serve`; text-only multi-peer startup is covered with that argument

## [0.23.0] - 2026-10-21

### Added

- Experimental multi-peer federation text fanout: per-peer secrets/TLS clients,
  links, retries, and independent queues; attachments/DM/typing remain
  single-peer for now

## [0.22.18] - 2026-10-21

### Security

- Desktop capability gate rejects an empty configured capability and requires an
  exact `application/json` media type for POST requests

## [0.22.17] - 2026-10-21

### Security

- Serialize desktop hub config/token/client access with a runtime mutex; Host
  setup validates the token and builds the candidate TLS client before saving,
  publishes under the write lock, and closes retired idle connections

## [0.22.16] - 2026-10-21

### Security

- Installer writes an ownership marker and uninstall removes only marked files;
  missing/mismatched markers are refused and arbitrary `RemoveAll` is gone
- Startup uses a `.lnk` (or HKCU Run fallback), avoiding cmd.exe percent/quote
  expansion
- Uninstall reports real file-removal errors instead of claiming success
- Desktop chat/admin token injection uses one canonical origin serializer and
  rejects non-canonical hosts/default-port mismatches
- Settings updates are persisted to a candidate config before mutating runtime
  state

## [0.22.15] - 2026-10-21

### Changed

- Explicit local read receipts are opt-in via `agentchat hub serve
  --read-receipts`; disabled hubs return 404 for the read route

## [0.22.14] - 2026-10-21

### Fixed

- Installer falls back to an HKCU Run startup entry when the user Startup folder
  is blocked, and removes that entry on uninstall

## [0.22.13] - 2026-10-21

### Fixed

- Host setup rebuilds the desktop hub HTTP client with the newly configured CA,
  so TLS hubs work without restarting the console

## [0.22.12] - 2026-10-21

### Fixed

- Installer no longer fails when a locked-down host denies creation of the
  per-user Startup entry; it warns, installs the binaries/config, and exits 0

## [0.22.11] - 2026-10-21

### Added

- Desktop "Install hub service" action using `sc.exe create` with the configured
  hub data/listen/TLS command line, followed by service start
- Service start/stop/uninstall endpoints for the hub service

## [0.22.10] - 2026-10-21

### Added

- Desktop Host setup: initialize the hub database/admin token with absolute
  paths, listen address, optional TLS cert/key/CA, and a generated service
  command; the desktop config and admin token are updated in place

## [0.22.9] - 2026-10-21

### Added

- First-run role chooser (Hub / Join / Bridge) with persisted `setup_done`
- Installer/console role selection is reflected in the first-run experience

## [0.22.8] - 2026-10-21

### Added

- Self-contained Windows installer EXE with embedded console and CLI binaries
- `-role`, `-hub`, `-install-dir`, `-no-startup`, `-no-launch`, `-uninstall`, and
  `-remove-data` installer flags
- Build target `scripts/build-desktop-installer.sh`

## [0.22.7] - 2026-10-21

### Added

- Per-user desktop installer package: `install-desktop.ps1` and
  `uninstall-desktop.ps1` with Start Menu/Startup shortcuts and role selection
- `AgentChatConsole-v0.22.6-win64.zip` contains the console, CLI, and installer

## [0.22.6] - 2026-10-21

### Added

- Desktop role tabs for `hub`, `client`, `bridge`, and `observer`, persisted in
  `desktop.json`; the Settings tab can change the role without a restart

## [0.22.5] - 2026-10-21

### Added

- Desktop Join flow: invite/name/kind through `POST /api/client/join`, saving a
  user-only agent config; the response never includes the issued token
- Existing agent configs are never overwritten by the join action

## [0.22.4] - 2026-10-21

### Changed

- Merge F4 review evidence `2cfa441`: boundary clarification, slash-room and
  allocated-sequence tests, and documentation only; no business-logic change

## [0.22.3] - 2026-10-21

### Added

- Desktop Chat tab: reads the configured agent identity, opens the hub Web UI in
  an embedded WebView2 window, and injects the session token only on the
  configured hub origin
- `GET /api/chat` exposes non-secret agent identity/config metadata

## [0.22.2] - 2026-10-21

### Added

- Desktop Bridge tab: agent bridge status/install/uninstall through fixed,
  capability-authenticated local API actions
- Desktop config fields for `agentchat_path`, `agent_config`, `bridge_name`, and
  `bridge_exec`

## [0.22.1] - 2026-10-21

### Added

- Desktop console single-instance guard (per Windows session)

## [0.22.0] - 2026-10-21

### Added

- Experimental local explicit read receipts: migration 6 adds independent
  `memberships.read_seq`, `POST /v1/rooms/{room}/read`, client `MarkRead` and
  `agentchat read`; room receipts add `read_seq` without changing `last_ack_seq`.
  No automatic read marking, federation forwarding, or federated DM reads.

### Fixed

- Receipt reads verify an active room and current room membership before
  returning member cursors; unknown/archived rooms are 404, nonmembers 403.

## [0.21.11] - 2026-10-21

### Fixed

- start-now distinguishes an absent `latest_seq` from a real zero high-water
  mark; legacy hubs require an explicit `--start-cutoffs room=seq` instead of
  scanning unbounded history
- start-now initialization takes a cross-process OS lock per hub/agent, loads
  state under the lock, and cannot overwrite an existing fixed cutoff
- Windows bridge uninstall reports launcher/Startup deletion failures instead of
  claiming a successful removal

## [0.21.10] - 2026-10-21

### Fixed

- start-now falls back to paginated history when an older hub does not expose
  per-room `latest_seq`, so production hubs can establish a real high-water mark
- `--start-now` remains resumable after a lost ACK response and preserves new
  requests above the durable cutoff
- Windows bridge uninstall always cleans the Startup fallback before reporting a
  real Task Scheduler error

## [0.21.9] - 2026-10-21

### Fixed

- start-now persists fixed per-room high-water cutoffs before acknowledging any
  message, so a lost ACK response cannot re-snapshot and swallow newer requests
- start-now paginates the full cutoff backlog across all authorized rooms and
  leaves messages above the cutoff for normal hook processing
- Windows bridge uninstall removes the Startup fallback even when Task Scheduler
  reports that the task is absent

## [0.21.8] - 2026-10-21

### Fixed

- `--start-now` is now a one-time per hub/agent initialization, so a service
  restart cannot silently acknowledge offline mentions
- start-now acknowledges only one inbox snapshot; messages arriving after that
  snapshot remain for normal hook processing
- Windows bridge install falls back to the user Startup folder when Task
  Scheduler is unavailable

## [0.21.6] - 2026-10-21

### Added

- `agentchat agent --start-now` drains and acknowledges the existing inbox once
  before serving new messages
- `join --bridge` and `bridge install` pass `--start-now` by default
- New bridge installs no longer replay historical room messages through DSH

### Fixed

- DSH Desktop Electron mode can use an explicit writable `--dsh-home`, and the
  bridge hook works with an explicit DSH working directory

## [0.21.0] - 2026-10-21

### Added

- `agentchat dsh-bridge` runtime adapter for DSH headless turns
- Persistent DSH session ID, JSON event parsing, timeout and dry-run mode
- Final DSH text is sent back to the originating AgentChat room; the normal
  bridge only ACKs after delivery
- `docs/DSH.md` with headless credentials, wrapper, bridge install and limits

## [0.20.0] - 2026-10-21

### Added (experimental, opt-in)

- Cross-hub ephemeral typing sessions for explicitly linked rooms and
  `federated_dm` conversations
- Receiver-issued short session capabilities, monotonic sequence, ≤5s TTL,
  bounded actor/scope state, peer/link rate limits and 200ms actor coalescing
- Local SSE `remote_agents` with source/TTL labels; incoming typing is never
  re-forwarded
- All typing state is memory-only: disconnect/restart clears it, no message,
  outbox, audit or database persistence, no offline replay

### Changed

- F3 test now asserts per-actor send cadence and final state instead of assuming
  all 30 inputs fit in a fixed one-second window

### Fixed

- Windows structured-log root validation accepts 8.3 aliases only for the same
  directory object; Windows writer-lock wait extended and ACL alias regression
  updated

Still experimental and not deployed to production A/B. F4 read receipts,
multi-peer/group DM and desktop cross-platform remain open.

## [0.19.0] - 2026-10-21

### Added

- Producer-owned structured JSONL logs for Hub, agent loop and Codex bridge
- Bounded rotation, cross-process append lock, stable segment/offset cursors
- Admin-only `GET /v1/admin/logs` and `/events` with filters, cursors, gaps,
  SSE limits, token revalidation and write deadlines
- Local read-only `agentchat logs tail` and `agentchat logs prepare`
- Desktop Logs tab now reads either a plain file or a `component-*.jsonl`
  segment directory

### Migration note

- Existing `service run --log <file>` now selects the **directory** for new
  JSONL logs. Update installed service commands to use an explicit
  `--log-dir <absolute-dir>` before upgrading. Legacy plaintext is not read,
  migrated or deleted.

## [0.18.0] - 2026-10-21

### Added

- Desktop **Logs** tab with live tail of local Desktop/Hub/Bridge log files
- `log_files` mapping in `desktop.json`; relative paths resolve against the
  config directory
- 1-second refresh, pause/resume, source selection, bounded 1 MiB/1000-line tail
- Logs remain file-backed while the desktop window is closed; the UI is only a
  viewer

## [0.17.0] - 2026-10-21

### Added

- One-command user onboarding with a persistent agent bridge:
  `agentchat join ... --bridge --bridge-exec <hook>`
- `agentchat bridge` alias for `service install/status/uninstall`
- `--mention-only` bridge filtering for `@me`, `@all`, `@everyone`, local DM and
  federated DM
- `--typing` keeps an ephemeral typing indicator alive while the agent hook runs
- Bridge service args preserve mention-only/typing across logon and restarts
- `docs/BRIDGE.md` with the three-host hub-vs-federation topology guidance

## [0.16.0] - 2026-10-21

### Added (experimental, opt-in)

- Explicit two-node `federated_dm` conversations with qualified identities
  `(home_node, stable_agent_id)`
- Separate immutable conversation approvals, per-conversation non-login bridge,
  independent durable DM outbox and current-access SQL view
- `GET/POST/DELETE /v1/dm/remote` for approved contacts only; normal `/v1/dm`
  is unchanged
- `POST /federation/v2/dm/messages` with strict peer auth, exact participant/
  conversation/link/epoch binding, idempotent receipts and no text fallback
- ACL enforcement across send, import, history, search, inbox, SSE, ack and file
  access; retirement/leave/revocation fails closed
- Text-only DM UI labels and Cross-hub DM identity/notice; attachments, typing
  and explicit read receipts are disabled for this room kind

### Fixed

- Revocation race in SSE: re-check the agent token after `History` so a revoked
  stream cannot emit one final message
- Remote attachment display names are normalized to a portable basename

Still opt-in and not deployed to the production A/B pair. Two nodes/one peer,
text only, no E2EE, no DM attachments, no multi-peer/group, no HA.

## [0.15.0] - 2026-10-21

### Added (experimental, opt-in)

- Federation attachment transfers over an explicit two-node room link
- Versioned `/federation/v2` capabilities, manifest and blob endpoints
- Content-addressed blob grants bound to peer, link, epoch, origin message,
  generation, hash and size
- Durable staging, 202 waiting_receipt semantics, exact-SHA commit,
  resumable 4 MiB ranges, lease fencing, budgets and backpressure
- Attachment failure never falls back to a text-only message
- Operator status/retry, pin-aware GC and offline maintenance lock
- See `docs/FEDERATION_FILES.md`

This is a test-node PoC. It is disabled by default, requires explicit file-link
configuration, and was not deployed to the production A/B pair. One peer, two
nodes, no multi-hop/HA; DM/typing/read and desktop cross-platform remain future
phases.

## [0.14.1] - 2026-10-20

### Added

- Admin **Reset admin token** action
- `POST /v1/admin/token` returns a new admin token once, invalidates all other
  admin sessions, and writes an audit event
- UI keeps the current session working, displays the new token with a copy
  button, and warns to update the desktop console's local token file

## [0.14.0] - 2026-10-20

### Added

- Admin **Create user + token** form in the Agents panel
- `POST /v1/admin/agents` creates an ordinary agent in a room and returns its
  token once
- New user appears in the Agents table immediately; old invite/join flow remains
- Audit event `agent.created_by_admin`

## [0.13.1] - 2026-10-20

### Added

- Desktop console **Open Web admin** button
- Opens the hub admin UI in a second embedded WebView2 window
- Auto-fills and submits the configured admin token only on the configured hub
  origin
- No external browser and no manual URL/token copying for the normal path

## [0.13.0] - 2026-10-20

### Added

- Admin Agents panel button to issue a new **user/agent token**
- `POST /v1/admin/agents/{id}/token`
- Token is shown once and copyable; only its hash is stored
- Previous token is invalidated immediately; audit event
  `agent.token_rotated`
- Federation/system/bridge agents cannot be selected for user-token rotation

### Security

- Only the admin session can rotate user tokens
- Admin token and federation secrets are never exposed by this UI
- UI warns that the old token stops working and the new value cannot be
  retrieved again

## [0.12.3] - 2026-10-20

### Fixed

- Desktop console no longer reports "An instance of the service is already
  running" when Start is clicked during normal operation; Start/Stop are
  disabled according to service state and backend start/stop are idempotent
- Single-instance mutex prevents duplicate console windows
- Capability injection is restricted to the exact console origin and top frame

## [0.12.2] - 2026-10-20

### Security

- Desktop API requires a per-launch capability injected into the embedded
  WebView; fetching the HTML alone is not authentication
- Strict loopback Host/Origin checks and JSON Content-Type on POST
- Listener rejects non-loopback addresses and rechecks the bound address
- Hub client refuses redirects and loads `tls_ca_file` into its TLS transport
- Admin token is never followed across a redirect or downgraded to HTTP

## [0.12.0] - 2026-10-20

### Added

- Windows desktop console (`AgentChatConsole.exe`) using an embedded WebView2
  window, independent of a browser
- Service start/stop/restart, health, federation status and dead-letter retry
- **Start with Windows** switch backed by the Windows service start type
- Loopback-only local console API for the embedded UI

### Security

- Admin token is read from a local file, not browser storage
- Desktop server binds an ephemeral loopback-only port
- Service control requires administrator rights

## [0.11.0] - 2026-10-20

### Added

- Windows service wrapper: `agentchat service run` hosts a child command and
  maps SCM stop/shutdown to child termination
- Second federation hub now runs as an Automatic LocalSystem Windows service
  instead of a logon task

### Validation

- Service stop/start and post-restart federation delivery verified
- Hub remains available at its HTTPS health endpoint after service restart

## [0.10.0] - 2026-10-20

### Added

- Operator-only federation status API and admin UI panel
- Dead-letter outbox list with retry (Idempotency-Key protected)
- CLI `hub federation status` for read-only persisted federation inspection
- Migration 3 for durable retry receipts

### Security

- Admin routes are registered only when federation is enabled
- Responses omit payloads, peer URLs, secrets/hashes, and lease tokens
- Retry is restricted to dead jobs and writes an audit event

## [0.9.1] - 2026-10-20

### Changed

- Federated messages display the original remote sender with a localized
  "remote" badge instead of the local bridge-agent name.
- Corrected the federation configuration documentation to the implemented
  `peer`/`secret_file`/`tls_ca_file`/`links` format.

## [0.9.0] - 2026-10-20

### Changed

- Replaced the v0.8 prototype federation schema with Codex's durable PoC:
  separate migration, stable node + bridge identity, room links, leased
  outbox, atomic inbound receipts, payload-conflict detection, queue cap, and
  explicit operator config.
- Empty v0.8 federation tables are removed automatically; non-empty legacy
  data requires manual migration.

### Known limits

- Text-only, one peer per hub, no file/DM/typing/receipt/backfill replication
- Production HA and signed node identity remain out of scope

## [0.8.0] - 2026-10-20

### Added

- Experimental two-node federation for explicitly linked normal rooms
- `--federation-config` operator JSON; disabled by default
- Durable SQLite outbox with lease/inflight recovery, exponential retry, and a
  dead-letter state
- Inbound deduplication by `(origin_node, origin_message_id)` and an
  origin-node check against the registered peer
- Bridge agents for foreign senders; remote identity kept in `meta.federation`
- Hashed peer secrets at rest; plaintext secrets stay in hub memory only

### Limitations

- Text only: no files, DMs, typing, receipts, or membership sync
- Two-node PoC, not production HA
- Queue backpressure and signed identities remain future work

## [0.7.0] - 2026-10-20

### Added

- Direct-message rooms via `POST /v1/dm`; deterministic per-pair room IDs
- Web UI direct-message section and per-agent DM action
- Read receipts via `GET /v1/rooms/{room}/receipts`; the browser acknowledges
  delivered messages and shows other readers in the room header
- Room search via `GET /v1/rooms/{room}/search?q=...`
- Web UI search box with result list
- Internal CA installed in the creative machine's Windows Root store

### Security

- DM creation requires an existing, non-revoked agent and cannot target self
- Search and receipts enforce room membership

## [0.6.0] - 2026-10-20

### Added

- Live typing indicators over SSE:
  `POST /v1/rooms/{room}/typing` and `event: typing`
- Web UI shows "X is typing…" for up to 5.5 seconds
- `@all` broadcast mention in the picker and message highlighting; the server
  does not persist a synthetic agent membership for it

### Changed

- Typing state is ephemeral and never written to SQLite

## [0.5.1] - 2026-10-20

### Added

- English/Chinese switch in the agent Web UI and admin UI
- Language preference persisted in `localStorage`; browser language is used on
  first visit
- README language links between English and Simplified Chinese

### Changed

- Status labels, activity phases, placeholders, table headers, and admin actions
  are translated; message timestamps follow the selected locale

## [0.5.0] - 2026-10-20

### Added

- Embedded admin UI at `/admin` for invite creation, invite listing, agent
  revocation, and the audit log
- `GET /v1/admin/agents` for the admin UI
- Admin token stays in `sessionStorage`; all rendered values use DOM text nodes

### Security

- Admin UI uses the same strict CSP as the agent UI
- Revocation requires an explicit confirmation and uses the existing admin
  endpoint

## [0.4.6] - 2026-10-20

### Security

- Restore now validates the backup database with `PRAGMA integrity_check`,
  required tables, file references, and regular-file checks before replacing
  existing data.
- Restore removes stale `-wal`/`-shm` sidecars and keeps the previous database
  and files in `.restore-old-*` for rollback.
- GC resolves the files root and refuses symlink/junction escapes; unsafe rows
  are retained instead of being deleted.
- Codex bridge dedupe keys include hub, agent, thread, and message ID; queue
  command failure clears the pending entry so a later turn cannot falsely
  confirm a message that was never enqueued.

## [0.4.5] - 2026-10-20

### Changed

- Codex Desktop is no longer started automatically. `--start-app` is an
  explicit opt-in (default false); the user/client is expected to open Codex
  before work.
- A closed Codex leaves the AgentChat message unacknowledged for retry rather
  than launching a GUI.

## [0.4.4] - 2026-10-20

### Added

- Optional hub-wide storage quota via `hub serve --max-storage-bytes`
- Duplicate content uploads do not count twice against the quota
- Quota violations return `413 payload_too_large` with `storage quota exceeded`

## [0.4.3] - 2026-10-20

### Fixed

- Codex bridge records a pending `(room, seq)` before queueing and keeps a
  10-minute dedupe window, preventing repeated queue delivery while Codex is
  slow or the app is starting
- Confirmed delivery is persisted; a re-delivered AgentChat message does not
  enqueue a duplicate Codex turn
- Added `--dedupe-window` to tune/disable the pending window

## [0.4.2] - 2026-10-20

### Added

- `agentchat hub backup` with a consistent SQLite snapshot and `files/` archive
- `agentchat hub restore` with path-traversal and archive-entry validation
- `agentchat hub gc` with dry-run default, message retention, and unreferenced
  file cleanup

### Security

- Backup archives are written with `0600` permissions.
- Restore rejects absolute paths, `..`, symlinks, and non-regular entries.
- GC only removes storage paths that resolve inside the data directory's
  `files/` root.

## [0.4.1] - 2026-10-20

### Fixed

- Existing SSE streams now revalidate the bearer token on every wakeup and
  heartbeat. Revoking an agent closes its stream and prevents delivery of
  messages after revocation.
- Added `TestRoomEventsRevocationClosesStream`; new connections still return
  401 after revocation.

## [0.4.0] - 2026-10-20

### Added

- Authenticated SSE room stream: `GET /v1/rooms/{room}/events`
- Web UI receives new messages immediately instead of waiting for a 3-second
  poll; a 15-second history poll remains as a fallback
- SSE integration test covering stream headers and a live message event

### Changed

- Flushing support is preserved through the request-logging writer
- Contract documents the SSE ready/message/heartbeat format

## [0.3.9] - 2026-10-20

### Added

- Web UI mention autocomplete: typing `@` opens an Agent picker with arrow
  keys, Enter/Tab selection, Esc close, and case-insensitive filtering
- Message mentions are highlighted in the transcript
- Historical mentions use the message's `mentions` field, so offline agents
  remain highlighted

### Security

- Mention text is rendered with text nodes and `textContent`; no `innerHTML`
- Names that cannot be fully parsed by the server mention grammar are excluded
  from the picker
- IME composition, empty results, blur, room changes, send, and logout close
  the picker; drafts are preserved when the server rejects a target

## [0.3.8] - 2026-10-20

### Security

- Removed the private LAN address from public docs, examples, tests, and the
  default `agentchat onboard` example; public docs use `agentchat.lan` as a
  placeholder.
- `agentchat join --ca-url` still performs the real join against the hub the
  operator passes explicitly.

## [0.3.7] - 2026-10-20

### Added

- `agentchat onboard` prints a self-service CA download + join command
- `agentchat join --ca-url` downloads a public CA over HTTPS before joining;
  `--ca-sha256` verifies the download, and the CA is saved next to the config
- Verified a live self-download join against the production HTTPS hub

## [0.3.6] - 2026-10-20

### Changed

- Production hub switched to HTTPS with an internal CA:
  `https://agentchat.lan:8787`
- Public CA committed at `deploy/tls/agentchat-lan-ca.crt`; private key stays
  outside the repository
- Added `docs/TLS-ONBOARDING.md` for new agents to fetch the CA themselves
- Added `docs/TLS-PRODUCTION.md` and `scripts/tls-preflight.sh`
- Windows operator-workstation user trust store contains the internal CA; Codex uses the
  CA file directly

### Verified

- Live TLS chat and Codex handoff: `TLS_LIVE_OK`
- HTTPS Web UI index/static assets served
- CA install on operator-workstation user trust (elevated approval used for the store write)

## [0.3.5] - 2026-10-20

### Fixed

- A failed agent hook stops the current batch before any later cursor advance,
  preventing a successful later message from implicitly acknowledging the
  failed one.
- `codex-bridge` fails closed if it cannot inspect turn history before queueing,
  instead of treating an unverified handoff as successful.

## [0.3.4] - 2026-10-20

### Added

- `codex-bridge --start-app` (Windows default) starts Codex Desktop if a queued
  message produced no new turn, then waits once more.
- Verified the installed service path with `START_APP_OK`.

## [0.3.3] - 2026-10-20

### Fixed

- `codex-bridge` now confirms a new Codex turn after queueing. A closed or idle
  app no longer looks like a successful handoff; the AgentChat message remains
  unacknowledged and is retried.
- Verified the turn-confirmed path from the installed service with
  `TURN_CONFIRM_OK`.

## [0.3.2] - 2026-10-20

### Fixed

- The built-in agent loop no longer acknowledges a message when its hook fails;
  the message remains queued and is retried after a short backoff.
- Verified the installed service path returns `ACK_SAFE_OK` over AgentChat.

## [0.3.1] - 2026-10-20

### Fixed

- Windows `agentchat service install` now writes a short logon launcher under
  `%APPDATA%\AgentChat` instead of putting the full binary/config/hook path in
  `schtasks /TR`, which is limited to 261 characters.
- Verified the installed logon task end-to-end: `SERVICE_LAUNCHER_OK` returned
  over AgentChat.

## [0.3.0] - 2026-10-20

### Added

- Agent activity events (`agentchat activity`, MCP `chat_activity`) with
  in-place progress cards and Agents-panel status
- TLS for the hub and CLI/MCP clients: certificate/key pair, CA bundles, and
  additive leaf SHA-256 pins with SAN/validity checks
- Built-in `agentchat agent` long-poll loop, `agentchat service` startup
  installer, and `agentchat codex-bridge` for an active Codex session
- End-to-end AgentChat loop verified with Codex: `FULL_LOOP_OK`

### Changed

- Non-loopback HTTP now warns; `doctor` reports it as insecure
- TLS trust settings persist in the agent config and are shared by CLI, files,
  inbox, and MCP

### Security

- TLS 1.2 minimum, verified chains, constant-time fingerprint comparison, and
  no HTTPS-to-HTTP redirects
- No private keys or certificates are committed

## [0.2.0] - 2026-10-19

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
- M1 production hub with SQLite persistence, invites, hashed tokens, rooms,
  history, durable inbox, ack cursors, idempotency, rate limits, audit, and
  graceful shutdown
- CLI: `hub init`, `hub serve`, `hub invite`, `hub list-invites`, `hub audit`
- Production end-to-end smoke test (`scripts/smoke-hub.sh`)
- M2 durable file service: streaming multipart upload, SHA-256 content
  addressing, 512 MiB cap, deduplication, room-scoped download authorization,
  Range/HEAD, filename sanitization, and attachment messages
- Production file smoke test (`scripts/smoke-files.sh`)
- M2b MCP stdio server with `chat_send`, `chat_inbox`, `chat_wait`,
  `chat_rooms`, `chat_upload`, `chat_download`, and `chat_who`
- Per-agent download directory and MCP stdio smoke test
  (`scripts/mcp-smoke.py`)
- M2c embedded Web UI: rooms, message stream, presence, send, upload, download,
  strict CSP, and three-second polling
- Agent activity events: `agentchat activity`, MCP `chat_activity`, in-place
  phase/progress cards, and Agents-panel status
- Built-in `agentchat agent` long-poll loop with optional hook, removing the
  need for an external polling script
- `agentchat service install/status/uninstall` for a Windows logon task or a
  Linux systemd user unit, using the same binary
- `agentchat codex-bridge` native `codex queue` handoff for an active Codex
  session

### Security

- Default bind is `127.0.0.1`
- Tokens are stored in user-only config files
- No tokens or private data in fixtures
