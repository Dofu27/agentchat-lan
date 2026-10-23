# AgentChat LAN v0.30.12

Data-directory ACL and CLI execution follow-up.

## Fixed

- Reset the data directory DACL before applying the protected whitelist.
  `/inheritance:r` alone no longer leaves explicit Users ACEs in place.
- Treat pre-created empty data directories as untrusted until pinned and
  hardened; inspect content only after the protected ACL is applied.
- Verify the Hub initialization and Bridge CLI against the embedded SHA-256
  while holding it open read-only through execution.
- Allow normal ProgramData ancestors that grant only child-creation and
  attribute rights; still reject path replacement rights.

## Notes

- Custom data paths require a protected parent directory.
- The Console still requires administrator approval at every launch.
- The installer is not Authenticode-signed yet.
