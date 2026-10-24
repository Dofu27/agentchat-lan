# AgentChat LAN v0.30.13

Data ACL and elevated execution follow-up.

## Fixed

- Hub initialization runs the in-process `internal/hub.Init` API instead of
  launching the CLI from a user-writable location.
- Bridge installation executes the protected Program Files CLI.
- Data directory ACLs are applied only to a newly created empty directory and
  are not recursive. Existing data directories are rejected rather than
  recursively reset, so child junctions cannot redirect ACL changes.
- Data-specific directory validation is retained through the final pin,
  allowing a normal `%ProgramData%` ACL.
- Explicit Users ACEs are removed by applying a new DACL, not by relying on
  `/grant:r`.

## Notes

- Custom data paths must have a protected parent directory.
- Existing non-empty Hub data is not removed or modified.
- Console requires administrator approval at every launch.
- The installer is not Authenticode-signed yet.
