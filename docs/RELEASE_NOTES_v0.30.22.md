# AgentChat LAN v0.30.22

Leave/reset deletion and service binary recovery fixes.

## Fixed

- Only the fixed `%LOCALAPPDATA%\AgentChat\agentchat.json` managed path can be
  deleted. Custom AgentConfig paths are detached without deletion.
- The managed parent chain is pinned and reparse points are rejected; the
  exact file handle is held through deletion.
- Deletion failure is returned to the UI instead of being ignored.
- Protected service binary replacement renames the old binary to a backup,
  verifies the new payload, and rolls back on failure.

## Notes

- Hub data, token files, and the Windows service remain preserved by
  leave/reset.
- The installer is not Authenticode-signed yet.
