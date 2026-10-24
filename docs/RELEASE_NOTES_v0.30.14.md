# AgentChat LAN v0.30.14

Atomic ACL and credential delivery follow-up.

## Fixed

- New Hub data directories are created with `CreateDirectoryW` and an explicit
  protected security descriptor. There is no longer a create-then-harden
  window.
- The admin token file is created with `CreateFileW`/`CREATE_NEW` and an
  explicit protected DACL owned by Administrators, then written, flushed with
  `Sync`, and closed before the Hub initialization commit.
- The data directory guard is held through initialization.

## Notes

- Existing data directories are rejected rather than modified.
- Custom data paths must have a protected parent.
- Console requires administrator approval at every launch.
- The installer is not Authenticode-signed yet.
