# AgentChat LAN v0.30.34

Installer ownership and certificate recovery fixes.

## Fixed

- Console installs its private CLI as `agentchat-console.exe`, separate from
  the Hub service's `agentchat.exe`.
- Certificate reset refuses to proceed if the existing cert/key cannot be
  backed up, reports restore failures, and serializes certificate operations
  with Hub setup/service actions.
- Inno Setup reports a visible error if `desktop.json` cannot be created or
  written.
- Configured Hub always shows the URL/TLS warning outside the collapsed body.

## Notes

- Existing Hub data and service are preserved.
- Installer is not Authenticode-signed.
