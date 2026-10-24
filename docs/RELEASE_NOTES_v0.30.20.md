# AgentChat LAN v0.30.20

Console settings recovery actions.

## Added

- **Leave current hub** removes the local agent configuration and returns the
  Console to the join flow.
- **Restore all defaults** clears Console role/Hub/agent settings and returns
  to first-run setup. Hub data, the database, and the Windows service are not
  deleted.

## Notes

- Both actions require confirmation.
- Admin token files and Hub data are preserved.
- The installer is not Authenticode-signed yet.
