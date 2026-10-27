# AgentChat LAN v0.30.52

Security remediation for the v0.30.51 review.

## Security

- Attachment directories require a protected, pinned parent chain. A weak
  parent such as `G:\work\ACL` is rejected.
- Clear Hub data uses the configured managed data path only; request-provided
  arbitrary paths are rejected.
- Clear Hub data fails closed when the service state is unknown or not stopped.
- Hub initialization creates the protected database directory before the
  default attachment directory.
- Existing DSH bridge configs must have `name=dsh`, `kind=dsh`, and an Agent ID
  different from the human Console identity.
- New DSH configs carry the configured TLS CA.
- DSH local-save failures leave a pending recovery config.
- Replace joins preserve and restore the previous agent config on failure.
- Token deletion failures return a partial failure instead of success.
- Generated PowerShell instructions use single-quoted literals.

## Installer

- Hub installation asks for the attachment directory and records `hub_files`
  in `desktop.json`.
- Default attachment directory is the protected
  `C:\ProgramData\AgentChat\files`; a larger protected local root can be
  selected explicitly.

## Notes

- A customer-selected large drive must have a protected parent chain. A plain
  user-writable folder is rejected by design.
- Installer remains unsigned.
