# AgentChat LAN v0.30.1

Patch release focused on making Host setup usable from the Console GUI.

## Added

- Guided Host setup with a five-step checklist.
- Generate a local TLS certificate and private key.
- Install the generated certificate into the current user's Windows Root store.
- Open or remove the Windows firewall rule for the Hub port.
- Existing Hub initialization, Windows service install, start/stop/restart, and
  start-with-Windows controls remain in the same Console.
- Windows VERSIONINFO metadata for `agentchat.exe`, `AgentChatConsole.exe`, and
  the installer.

## Notes

- Firewall changes require running the Console as administrator.
- Clients connecting over HTTPS must trust the generated certificate; the
  generated certificate can be installed on each client with the Console button.
- The SQLite Hub data directory must remain on a local fixed disk. Do not use a
  mapped network drive for the live database.
- The Windows installer is not Authenticode-signed yet. SignPath Foundation
  application is pending.

## Installer

`AgentChatConsole-v0.30.1-installer.exe`

See release assets for platform archives and checksums.
