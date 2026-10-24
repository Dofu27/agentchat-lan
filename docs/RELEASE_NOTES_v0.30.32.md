# AgentChat LAN v0.30.32

Certificate reset button.

## Added

- Host setup **Reset certificate** removes the trusted certificate, regenerates
  the TLS certificate/key, and installs the new certificate for the current
  user. Restart the Hub service afterward.

## Notes

- The button requires confirmation.
- Existing Hub data and agent configs are preserved.
- Installer wizard and bundled WebView2 remain available.
