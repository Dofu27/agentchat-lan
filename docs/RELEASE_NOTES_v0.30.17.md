# AgentChat LAN v0.30.17

Host setup and navigation usability release.

## Added

- Windows file/folder pickers for data directory, admin token, certificate,
  private key, bridge binary, and agent config.
- Copyable agent join command template in Host setup.
- Settings sub-navigation for Bridge, Admin, Logs, and About.

## Changed

- HTTPS Hub URLs require a TLS certificate and key.
- Host setup stays visible after initialization and is greyed out.
- An installed Windows service disables the Install button and uses Restart.
- Protected service binary installation writes directly without a temporary
  file rename.

## Notes

- Existing Hub data and service configuration are preserved.
- The installer is not Authenticode-signed yet.
