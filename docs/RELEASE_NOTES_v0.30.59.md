# AgentChat LAN v0.30.59

DSH attachment handoff.

## Added

- DSH bridge downloads every attachment with its own agent token.
- Files are placed under the DSH workdir:
  `.agentchat-attachments\<message-id>\`.
- The DSH prompt includes the downloaded local paths.
- Attachment-only messages are accepted.

## Notes

- Attachment downloads are verified by the existing Hub SHA-256 check.
- Downloaded files remain available to DSH during the run.
- Installer remains unsigned.
