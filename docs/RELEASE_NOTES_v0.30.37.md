# AgentChat LAN v0.30.37

Host setup feedback and data recovery fixes.

## Fixed

- The Restart as administrator button is disabled when the Console already has
  administrator rights.
- Auto-detect displays the selected URL next to the button.
- Certificate installation no longer shows raw certutil output; it shows a
  clean status and fingerprint.
- Firewall operations show working, success, and failure feedback.
- Host setup has a confirmed **Clear Hub data** action. It refuses to run while
  the Hub service is running, refuses volume roots/non-managed paths, and
  preserves the admin token, service, and desktop config.

## Notes

- Stopping `AgentChatHub` is still required before clearing its data.
- Installer remains unsigned.
