# AgentChat LAN v0.30.9

Host setup diagnostics release.

## Added

- The header shows `administrator` or `standard user`, based on the actual
  Windows process token.
- Host setup rejects `0.0.0.0` / `::` as the Hub URL. These are listen
  addresses, not addresses a client can dial.
- Hub data is rejected if it is placed inside the per-user AgentChat install
  directory. The default data path now uses ProgramData.
- Host initialization, certificate, and firewall errors also raise a
  notification instead of only changing inline text.

## Fixed

- Protected service installation errors now include the underlying Windows
  error and path, so a failed rename is diagnosable.

## Notes

- The Console still requires administrator approval at every launch.
- Existing Hub data and configuration are preserved by the installer.
- The Windows installer is not Authenticode-signed yet.
