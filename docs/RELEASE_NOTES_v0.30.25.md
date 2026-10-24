# AgentChat LAN v0.30.25 Offline

Offline installer with Microsoft Edge WebView2 Runtime x64.

## Added

- Bundled WebView2 Runtime installer.
- The installer checks for WebView2 first and skips the bundled runtime when it
  is already present.
- If missing, it extracts and runs the official runtime installer with a UAC
  prompt, waits for completion, and verifies the runtime path before launching
  the Console.

## Notes

- This package is substantially larger than the normal installer because it
  contains the WebView2 offline runtime.
- The AgentChat executable is not Authenticode-signed yet.
