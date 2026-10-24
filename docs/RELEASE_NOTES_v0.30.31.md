# AgentChat LAN v0.30.31

Hub URL warning UI.

## Added

- Host setup always shows: with multiple adapters or VPNs, verify the selected
  address; regenerate the certificate if the IP changes.
- Dynamic warning when multiple LAN addresses are detected.
- Dynamic warning when the TLS certificate does not include the selected IP.

## Notes

- Auto-detection still prefers an IP present in the certificate SAN.
- The installer wizard and bundled WebView2 remain available.
