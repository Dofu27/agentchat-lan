# AgentChat LAN v0.30.58

TLS and Clear hardening.

## Security

- Hub TLS defaults to `C:\ProgramData\AgentChat\tls`.
- TLS directory and private material use protected DACLs.
- Clear Hub data moves the managed directory to a quarantine name before
  recursive removal.
- If quarantine removal fails, the quarantine path is returned for recovery.

## Notes

- Existing LocalAppData TLS certificates are not automatically migrated.
- Quarantine still uses path-based rename; full handle/FileId binding remains
  future work.
- Installer remains unsigned.
