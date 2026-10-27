# AgentChat LAN v0.30.48

Split database and attachment storage.

## Added

- Host setup has separate fields:
  - Database directory — protected, small SQLite database and metadata.
  - Attachment directory — large uploads, may be on another local disk.
- Hub accepts `--files-dir`.
- The generated Windows service command passes `--files-dir`.
- The attachment directory is created with an admin/SYSTEM-only DACL when new.

## Notes

- The database remains in the protected `hub_data` directory.
- The attachment path does not require every ancestor to be protected, because
  it holds only uploaded blobs, not service credentials.
- Existing installs default to `<hub_data>/files` until a new directory is
  selected during initialization.
- Installer remains unsigned.
