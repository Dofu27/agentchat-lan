# AgentChat LAN v0.30.54

Second review remediation.

## Security

- Attachment ACLs use `MAXIMUM_ALLOWED`; child-create and inherited
  Authenticated Users/Everyone write ACEs are rejected.
- Token read failures other than not-found fail closed during Clear.
- Initialization cleanup errors are returned.
- DSH pending write failures retain both the original failure and the recovery
  failure.
- TLS CA resolution is shared by join and existing DSH config paths.
- Agent-config rename rollback failures return backup and restore errors.

## Notes

- Existing attachment directories with inherited user write ACEs must be
  recreated or hardened before use.
- Installer remains unsigned.
