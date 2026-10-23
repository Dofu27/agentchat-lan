# AgentChat LAN v0.30.6

Final Host service installer hardening follow-up.

## Fixed

- Directory ACL checks now allow ordinary users to create unrelated
  subdirectories while still rejecting deletion, rename, DACL, and owner
  changes on pinned components. This fixes Host setup failing at `C:\`.
- The elevated replacement process still creates and holds the single-instance
  mutex; it only skips activating the old window.
- Full parent-chain pinning, known-folder Program Files resolution, verified
  CLI digest, and the verified upgrade source from v0.30.5 are retained.

## Notes

- Service installation requires administrator rights.
- Existing Hub data is not modified.
- The Windows installer is not Authenticode-signed yet.
