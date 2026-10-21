# Production rollout and rollback

Status: prepared, not executed. A and B still run v0.14.1.

This runbook upgrades the two production hubs one at a time. It requires an
explicit maintenance window; do not run it while agents are actively working.

## Target

- Release: `v0.22.13` or the latest tagged release whose CI is green.
- Federation: **pause the link for the maintenance window** (operator decision
  2026-10-21). Production A currently reports one enabled peer and one enabled
  `general`→`general` room link, with delivered outbox rows. Do not silently
  drop the link or its queue; back up the federation tables, pause the link
  before upgrading B/A, and resume only after both nodes pass acceptance.
- Read receipts: available but not auto-marked; no client depends on them.
- DSH bridge: paused for the upgraded hub, then restarted and re-tested.

## Preflight

For each hub:

```sh
agentchat --version
curl -fsS https://<hub>/healthz
curl -fsS https://<hub>/readyz
```

Record:

- installed binary and service/task names
- data directory and free space
- TLS certificate/key paths and expiry
- admin token file location (never print its contents)
- current federation state (must remain disabled)
- number of rooms, agents, and files
- the exact rollback binary checksum

## Preflight evidence

On 2026-10-21 the following non-disruptive checks passed:

- A `/healthz`: `status=ok`, `version=v0.14.1`
- A `/readyz`: `status=ready`
- B `/healthz`: `status=ok`, `version=dev` (record the binary checksum before
  upgrading because the version string is not a release tag)
- B `/readyz`: `status=ready`
- `agentchat hub backup --data ... --out ...`: 336 KiB archive
- backup SHA-256: `43f09a5c0703860db6625e3d7d1a3cb7ed3414dc2e54acbe6e30e99bc87c730d`
- restore into a temporary directory: succeeded
- restored `hub federation status`: node, one peer, one room link, and
  `delivered=100` outbox rows present
- upgrade rehearsal: restored the same v0.14.1 backup into temporary
  directories and started the staged binaries read-only. `v0.26.4` passed, and
  the final `v0.29.0` rehearsal on `127.0.0.1:18793` returned
  `{"status":"ok","version":"0.29.0"}` and `{"status":"ready"}` with the
  federation peer/link/outbox state readable. No production service was
  touched. v0.28.1 had passed the same rehearsal on port 18792.

The backup was copied to an operator-only directory with mode `0600`; it
contains tokens and federation secrets and must never be committed.

## Staged release

On 2026-10-22 `v0.29.0` was staged read-only on A at
`/home/operator/agentchat-staging/v0.29.0/agentchat` (mode 0700, version checked).

A runs the hub in Docker (`docker ps` shows `agentchat:m141-admin-reset`), not
from a host binary. A staged image was built without replacing the running
container:

```text
agentchat:v0.29.0  (FROM agentchat:m141-admin-reset + /usr/local/bin/agentchat)
docker run --rm ... version → agentchat 0.29.0
```

For B, the same image is exported as a gzipped Docker archive:

```text
/home/operator/agentchat-staging/agentchat-v0.29.0-image.tar.gz  18M
sha256 09825c26afc3fa17c8aa8bd1433a347ef22c98cf365102b0f255cdd440c0d000
```

The v0.29.0 image was also run against a **copy** of A's `/home/operator/agentchat-data`
and `/home/operator/agentchat-tls` on `127.0.0.1:18795` (federation config omitted),
returning `{"status":"ok","version":"0.29.0"}` and `{"status":"ready"}`. The
running container was not replaced.

The maintenance upgrade must preserve A's current container mounts, ports,
user, and TLS/federation flags. Record `docker inspect agentchat` before
stopping it. B's runtime is not yet verified from this host.
No service was stopped or replaced. B staging was not possible from this host
because the A→B SSH key is denied; use B's own console/SSH path during the
maintenance window.

## Backup

Stop writes first (maintenance window), then back up the SQLite database, WAL,
config, TLS material, federation config, and blob directory.

Preferred online backup when the hub remains running:

```sh
sqlite3 <data>/agentchat.db ".backup '/backup/agentchat-$(date +%Y%m%dT%H%M%S).db'"
sha256sum <data>/agentchat.db /backup/agentchat-*.db
```

Offline backup is safer for the first rollout:

1. stop the hub service/task;
2. copy the whole data directory, including `agentchat.db-wal` and `-shm`;
3. copy config, TLS, federation files, and admin token file with permissions;
4. run `sqlite3 <backup>/agentchat.db 'PRAGMA integrity_check;'`;
5. start the old hub and verify `/readyz` before changing binaries.

Keep at least one backup that predates the upgrade and is stored outside the
hub data disk.

## Upgrade order

1. Upgrade **B** first. Keep federation disabled.
2. Verify B: health/ready, admin login, room history, send/inbox/ACK, file
   upload/download, MCP, logs, and a fresh agent join.
3. Start the DSH bridge against B only if B is the bridge's hub; verify one
   `@dsh-main` round trip.
4. Upgrade **A**. Repeat the same checks.
5. Reconnect federation only after both nodes pass and a separate trust review.
6. Resume agents and remove the maintenance notice.

## Rollback

If any check fails:

1. stop the new hub;
2. restore the previous binary (checksum recorded during preflight);
3. restore the database backup only if migrations or data are suspect;
4. start the old hub and verify `/readyz`;
5. keep the failed data directory for diagnosis, do not delete it;
6. record the failure and stop the rollout.

Never run a destructive SQLite down-migration. The additive schema changes from
the release are safe for the previous binary; database restore is the fallback
when data integrity is uncertain.

## Acceptance

- `/healthz` and `/readyz` return 200 on A and B.
- A and B report the expected version.
- Existing history and ACK cursors survive.
- A new message is delivered and acknowledged exactly once at the application
  layer.
- File upload/download checksum matches.
- Admin audit and logs are readable.
- DSH bridge replies once and does not replay old messages.
- Rollback binary and backup checksums are recorded.
- Federation remains disabled unless separately approved.

## Ownership

- Operator: gives the maintenance window and approves A/B changes.
- DSH: prepares binaries, checksums, and rollback commands.
- Codex: reviews the runbook and any migration/rollback risk.
