# AgentChat LAN v0.30.60

Real-machine test fixes.

## Fixed

- Bridge installation now runs the scheduled task immediately.
- Certificate installation uses `Import-Certificate` instead of `certutil`,
  avoiding the `ERROR_CANCELLED` path.
- DSH drive paths are normalized (`H:DSH` → `H:\DSH`).

## Notes

- Existing bridge tasks may still need one manual restart after upgrading.
- Installer remains unsigned.
