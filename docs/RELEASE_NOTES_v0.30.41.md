# AgentChat LAN v0.30.41

Clear-data path fallback.

## Fixed

- **Clear Hub data** uses the current Host setup data field when the saved
  desktop config has no `hub_data`. This covers configs left by an interrupted
  or manually edited setup.

## Notes

- Clearing still requires confirmation and stops the Hub service.
- It removes the data directory and managed admin token, then clears the data
  path so initialization is enabled again.
- Installer remains unsigned.
