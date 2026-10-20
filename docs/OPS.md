# Backup, restore, and garbage collection

These commands operate on a hub data directory (`agentchat.db` plus `files/`).
Take a backup before any destructive maintenance.

## Backup

```bash
agentchat hub backup --data /data --out /secure/agentchat-$(date +%F).tar.gz
```

The database is snapshotted with SQLite `VACUUM INTO`, so committed rows are
consistent even with WAL enabled. `files/` is added to the same tar.gz.
Use `--force` only when intentionally replacing the destination.

## Restore

Stop the hub first, then:

```bash
agentchat hub restore --data /data --in /secure/agentchat.tar.gz --force
```

`--force` replaces `agentchat.db` and `files/` under `--data`. The archive
reader rejects absolute paths, traversal entries, symlinks, and non-regular
files. Start the hub and verify with `agentchat hub audit` and a test message.

## Garbage collection

Dry-run first:

```bash
agentchat hub gc --data /data --days 30
```

- `--days 0` keeps all messages and only removes files with no attachment row.
- `--days 30 --apply` deletes messages older than 30 days, their mentions and
  attachments, and any file blobs no longer referenced.
- Without `--apply`, nothing is changed.
- File deletion is permanent; take a backup first.
- Stop the hub for the safest run. Running it live is possible but a download
  that races with GC can fail.

## Contract

These are operator commands, not `/v1` endpoints. No protocol change is
required.
