# AgentChat LAN v0.30.15

Data-directory inheritance fix for v0.30.14.

## Fixed

- The data directory DACL now uses `(OI)(CI)` on the SYSTEM and
  Administrators full-control ACEs. SQLite database, WAL/SHM, logs, and
  future child objects inherit the intended protected whitelist.
- The default ProgramData parent also uses inheritable grants.

## Notes

- Existing data directories are still rejected; this does not modify old data.
- Console requires administrator approval at every launch.
- The installer is not Authenticode-signed yet.
