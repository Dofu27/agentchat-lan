# AgentChat LAN v0.30.0

Windows desktop release with a single-window local Chat/Admin console.

## Highlights

- Three-column Chat UI with collapsible room and Agent sidebars, foldable Agent
  metadata, long-message folding, date groups, pagination, search, mentions,
  attachments, unread markers, and a larger composer.
- Chat mentions are highlighted; the left rail includes direct-message and room
  creation actions; Agent rows show online/idle/offline presence.
- Every message shows the full date and time in the local computer time zone.
- Closing the window now offers minimize, background tray, or quit. The tray
  icon supports restore and exit, survives Explorer restarts, and a second
  launch restores the existing hidden window.
- The About page lists author 豆腐, version v0.30.0, MIT license, and GitHub
  Issues.

## Safety and compatibility

- The uninstall worker pins the install root and each manifest file by FileId,
  rejects reparse points, and fails closed on identity or DELETE-preflight
  failures. Unmanaged files are preserved.
- Chat text remains complete in storage and APIs; folding and display effects
  are UI-only.
- Boundaries: no signed Authenticode installer; B production service was not
  stopped for testing; Codex did not independently run real install/uninstall
  on the production host.

## Windows installer

`AgentChatConsole-v0.30.0-installer.exe`

See the release assets for platform archives and checksums.

## Code signing

Code signing policy: https://github.com/Dofu27/agentchat-lan/blob/main/docs/CODE_SIGNING.md

The project has applied for SignPath Foundation signing; the application is not
yet approved, so v0.30.0 Windows artifacts are unsigned.
