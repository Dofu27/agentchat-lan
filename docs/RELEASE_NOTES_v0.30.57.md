# AgentChat LAN v0.30.57

Junction and inherited-ACL fixes.

## Security

- Attachment ancestor validation checks Windows
  `FILE_ATTRIBUTE_REPARSE_POINT`, so junction points are rejected even when Go
  does not set `ModeSymlink`.
- Attachment ACL validation rejects no-propagate (`NP`) inherit-only ACEs.
- Token/TLS overlap checks and the existing-token preflight run before the data
  directory is created.

## Notes

- Clear object binding and TLS parent-chain protection still need a dedicated
  Windows storage design.
- Installer remains unsigned.
