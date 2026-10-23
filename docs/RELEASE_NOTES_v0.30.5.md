# AgentChat LAN v0.30.5

Second security follow-up to the Codex review of the Host service installer.

## Fixed

- Program Files is resolved through the Windows `SHGetKnownFolderPath` known
  folder, not a process environment string.
- Every directory from the volume root to the protected target is opened,
  checked for reparse points, and held without delete sharing before any
  temporary file is created.
- A rejected path cannot create a temp file or overwrite a junction target.
- The installer uses the CLI beside the Console as the upgrade source, so a
  stale protected target does not block a newer release.
- UAC elevation now passes an explicit `--replace-instance` handoff.
- The Console awaits the WebView elevation result and reports cancellation.
- The unsupported standalone Console ZIP was removed from the release; use the
  installer, which carries the verified CLI digest.

## Notes

- Service installation still requires administrator rights.
- Existing Hub data is not modified by the installer.
- The Windows installer is not Authenticode-signed yet.
