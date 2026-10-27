# AgentChat LAN v0.30.56

Review remediation and clean rebuild.

## Security

- Non-sensitive mode rejects UNC paths, mapped network drives, volume roots,
  and ancestor reparse points.
- Default attachment ACL checks reject OIIO, CIIO, and OICIIO inherit-only ACEs.
- Reset all clears `allow_unprotected_files` and `hub_files`.
- Admin token and TLS files may not be inside the unprotected attachment tree.
- CA backfill writes its temporary file with a protected DACL.

## Build

- The release payload is rebuilt from the clean exact tag and must report
  `vcs.modified=false`.

## Notes

- Installer remains unsigned.
