# AgentChat LAN v0.30.33

Installer build and certificate safety fixes.

## Fixed

- Inno Setup build now builds the final CLI first and injects its SHA256 into
  the Console.
- Console payload installs under `Program Files\AgentChat Console`, separate
  from the Hub service binary directory.
- Inno config is written to `%APPDATA%\AgentChat`, matching the Console's
  default config location.
- Certificate reset preserves the previous cert/key until the new certificate
  installs successfully; failures are reported.
- Configured Hub exposes a certificate reset action.
- LAN auto-detection always proposes HTTPS and warns when TLS is not configured.

## Notes

- Existing Hub data and service are preserved.
- Installer is not Authenticode-signed.
