# AgentChat LAN v0.30.55

Non-sensitive attachment mode.

## Added

- Host setup option: **Allow a user-writable attachment folder**.
- Intended only for images, archives, and other non-sensitive attachments.
- Database, chat history, admin token, and TLS material remain protected.
- The attachment path still rejects network/root/reparse-point locations.
- Clear Hub data never deletes the external attachment directory.

## Risk

- Local users may read, replace, or delete attachment files.
- This mode is not a protected storage boundary; it is an explicit tradeoff for
  large non-sensitive files.
- Installer remains unsigned.
