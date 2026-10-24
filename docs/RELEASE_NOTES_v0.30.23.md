# AgentChat LAN v0.30.23

Managed-path and service-upgrade recovery fixes.

## Fixed

- A failure pinning the managed AgentChat parent chain returns an error and
  does not clear configuration or credentials.
- Service binary replacement asks SCM for the service state and refuses to
  move a running binary.
- Rollback failures are reported with the backup path.
- Backup cleanup failure is reported.

## Notes

- Hub data, token files, and the Windows service remain preserved by
  leave/reset.
- The installer is not Authenticode-signed yet.
