# AgentChat LAN v0.30.53

Additional review remediation.

## Security

- Attachment leaf ACLs reject ordinary-user write and inherited child-write
  access.
- Clear verifies a persisted Hub identity marker and the current admin token
  before deleting.
- Clear checks both the configured service and AgentChatHub, failing closed on
  query errors and non-stopped states.
- Failed attachment preparation removes only the data directory created by the
  same initialization attempt.
- DSH bridge install no longer falls back to the human agent config.
- Pending DSH configs are validated before replacing an existing file and are
  written with a protected DACL.
- TLS CA paths are resolved relative to the desktop config.
- Rollback failures are reported with the backup location.

## Notes

- Existing data directories without a Hub marker must be re-initialized before
  Clear is allowed.
- Installer remains unsigned.
