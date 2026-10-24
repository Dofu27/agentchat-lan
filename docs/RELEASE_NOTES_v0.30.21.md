# AgentChat LAN v0.30.21

Settings safety fixes from v0.30.20 review.

## Fixed

- Leave/reset only delete the managed `agentchat.json`; other configured paths
  are detached without deletion.
- Configuration is saved before live token state is cleared or the agent file
  is removed.
- Folder selection uses the FolderBrowserDialog API correctly.
- Reset clears stale Host form values before repopulating defaults.
- Service setup and leave/reset are serialized.
- A stopped protected service binary can be replaced; a running one produces
  an actionable error.

## Notes

- Hub data, database, admin token, and the Windows service remain preserved by
  leave/reset.
- The installer is not Authenticode-signed yet.
