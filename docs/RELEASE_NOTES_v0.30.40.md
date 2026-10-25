# AgentChat LAN v0.30.40

Host setup controls and Hub reset fixes.

## Fixed

- TLS and firewall controls are no longer disabled after Hub initialization.
- Clear Hub data can stop the configured/default service, removes the managed
  admin token, clears the configured data path, and enables re-initialization.
- Certificate, firewall, and clear-data status appears outside the collapsed
  Host setup body.
- Service binary replacement checks both the configured service name and the
  default `AgentChatHub` service.

## Notes

- Clearing Hub data is destructive; the confirmation dialog is required.
- The Windows service and desktop config are preserved.
- Installer remains unsigned.
