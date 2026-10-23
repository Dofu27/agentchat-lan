# AgentChat LAN v0.30.11

Data-directory security follow-up to v0.30.10.

## Fixed

- Uses the trusted System32 `icacls.exe` rather than PATH lookup.
- Pins the existing ancestors before creating the ProgramData parent and
  keeps the directory guard open until initialization completes.
- Creates or adopts the Hub data directory with an explicit protected DACL
  before SQLite opens it. Users do not receive write access through inherited
  parent ACEs.
- Rejects a non-empty existing data directory.
- Accepts normal ProgramData ancestors that grant only create-child rights,
  while still rejecting delete, rename, DACL, and owner changes.
- Removes IPv6 zone identifiers before rejecting unspecified addresses.

## Notes

- Custom Hub data paths still require a protected parent directory.
- Console requires administrator approval at every launch.
- The installer is not Authenticode-signed yet.
