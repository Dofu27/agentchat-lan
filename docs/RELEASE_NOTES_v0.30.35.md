# AgentChat LAN v0.30.35

CLI migration and installer ownership fix.

## Fixed

- Service install, Bridge, and connector setup prefer the bundled
  `agentchat-console.exe`; an old adjacent `agentchat.exe` or old config path
  no longer overrides it.
- The installer refuses the legacy directory layout instead of inheriting its
  uninstall-log ownership, and uses a new AppId for the new layout.
- CLI runtime version, PE resources, and Inno package version are generated
  from the same version source.

## Notes

- Existing Hub data and service are preserved.
- Installer is not Authenticode-signed.
