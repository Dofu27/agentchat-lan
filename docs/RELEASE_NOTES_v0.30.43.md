# AgentChat LAN v0.30.43

Orphan agent config self-join fix.

## Fixed

- One-click self-join uses `replace:true`: an existing `agentchat.json` is
  renamed to a timestamped `.bak-*` file before the new agent config is saved.
  Manual Join still refuses to overwrite an existing config.

## Notes

- The invite is created before join; a failed join can leave a short-lived
  single-use invite in Admin.
- Installer remains unsigned.
