# Production rollout and rollback

Status: prepared, not executed. A and B still run v0.14.1.

This runbook upgrades the two production hubs one at a time. It requires an
explicit maintenance window; do not run it while agents are actively working.

## Target

- Release: `v0.22.13` or the latest tagged release whose CI is green.
- Federation: disabled during the upgrade.
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
