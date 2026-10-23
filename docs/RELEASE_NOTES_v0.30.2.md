# AgentChat LAN v0.30.2

Hotfix release for v0.30.1.

## Fixed

- Restored the Console language strings to valid UTF-8.
- Fixed a JavaScript syntax error that made Host setup buttons and other
  Console controls unresponsive.
- Restored the English and Chinese README files to valid UTF-8.

## Includes v0.30.1 features

- Guided Host setup.
- Certificate generation and current-user certificate installation.
- Firewall rule open/remove buttons.
- Hub initialization and Windows service controls.
- Version metadata for desktop, CLI, and installer binaries.

## Notes

- Firewall and service changes require running the Console as administrator.
- The SQLite Hub data directory must be on a local fixed disk.
- The Windows installer is not Authenticode-signed yet.
