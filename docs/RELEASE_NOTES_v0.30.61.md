# AgentChat LAN v0.30.61

Bridge install without stopping Hub.

## Fixed

- Bridge install uses the Console-bundled `agentchat-console.exe`.
- DSH hook generation uses the same CLI.
- This avoids replacing the protected Hub service binary while AgentChatHub is
  running.

## Notes

- The protected Hub service binary is still used by the Hub service itself.
- Installer remains unsigned.
