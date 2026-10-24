# AgentChat LAN v0.30.30

Automatic LAN Hub URL detection.

## Added

- `/api/network` reports local IPv4 addresses and a preferred URL based on the
  TLS certificate SAN.
- Host setup fills an empty Hub URL automatically and provides an Auto-detect
  button.

## Notes

- Certificate SAN matching is preferred; if no match exists, the first private
  IPv4 address is used.
- The installer wizard and bundled WebView2 remain available.
- The installer is not Authenticode-signed yet.
