# AgentChat LAN v0.30.36

Legacy installer migration guard.

## Fixed

- The installer refuses any target containing `agentchat.exe`, including mixed
  old/new directories.
- It also detects the legacy AppId uninstall registration.
- The refusal explains the safe order: stop AgentChatHub, back up the service
  binary, run the old uninstaller, verify the service binary remains, then
  install this version.

## Notes

- This is a refusal-based migration, not automatic removal of old uninstall
  records.
- Existing Hub data and service are preserved.
- Installer is not Authenticode-signed.
