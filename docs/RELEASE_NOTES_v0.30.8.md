# AgentChat LAN v0.30.8

Administrator-launch release.

## Changed

- `AgentChatConsole.exe` now has a `requireAdministrator` Windows manifest.
- Every Console launch, including Start menu and startup entries, prompts for
  UAC approval.
- The installer launches the Console through ShellExecute `runas`. If the user
  cancels UAC, the installation still succeeds and a warning explains how to
  start it from the Start menu.

## Notes

- Service and firewall setup no longer require the separate
  "Restart as administrator" step.
- Existing Hub data and configuration are preserved by the installer.
- The Windows installer is not Authenticode-signed yet.
