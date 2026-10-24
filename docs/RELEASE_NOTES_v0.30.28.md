# AgentChat LAN v0.30.28

Setup launch fix.

## Fixed

- The finish-page launch now uses `shellexec`, so the Console's
  requireAdministrator manifest prompts for UAC instead of failing with
  CreateProcess error 740.

## Notes

- The installer itself is elevated; launching the Console is a second
  administrator action and prompts separately.
- WebView2 offline runtime is bundled.
- The installer is not Authenticode-signed yet.
