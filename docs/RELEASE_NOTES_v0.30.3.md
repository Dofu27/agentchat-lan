# AgentChat LAN v0.30.3

Host-service setup fix.

## Fixed

- Hub service installation now copies the CLI to
  `C:\Program Files\AgentChat\agentchat.exe` before registering the Windows
  service. The previous build rejected the per-user AppData path with
  "service executable must be in a protected machine directory".
- Added a **Restart as administrator** action in Host setup, so service and
  firewall setup can be performed in the Console without opening PowerShell.
- Restored valid UTF-8 Console strings and the Host setup buttons.

## Notes

- If the protected copy cannot be created, run the Console as administrator.
- Existing Hub data is not modified by the installer.
- The SQLite data directory must stay on a local fixed disk.
- The Windows installer is not Authenticode-signed yet.
