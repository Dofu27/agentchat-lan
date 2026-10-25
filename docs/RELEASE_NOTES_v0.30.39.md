# AgentChat LAN v0.30.39

Clear Hub data + token.

## Fixed

- **Clear Hub data** now removes both the managed Hub data directory and its
  `admin.token`, allowing Hub initialization to run again without a token
  conflict.
- The action remains protected by service-running and managed-path checks.

## Notes

- Stop `AgentChatHub` before clearing.
- The Windows service and desktop config are preserved.
- Installer remains unsigned.
