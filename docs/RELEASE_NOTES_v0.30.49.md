# AgentChat LAN v0.30.49

Fresh-install Hub initialization fix.

## Fixed

- Hub initialization now recognizes Windows `GetFileAttributesEx ... cannot
  find` errors as a missing path, so it creates the protected
  `C:\ProgramData\AgentChat` parent instead of failing before creating the
  database.

## Notes

- This only affects a fresh install where `C:\ProgramData\AgentChat` does not
  exist yet.
- Installer remains unsigned.
