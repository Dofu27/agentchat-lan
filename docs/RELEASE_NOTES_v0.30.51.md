# AgentChat LAN v0.30.51

Separate join/create instructions.

## Added

- **Agent join instruction** is generated from the current Hub URL, TLS
  certificate, and an optional one-time invite code.
- **Hub creation instruction** is generated from the current database,
  attachment directory, listen address, token path, and TLS paths.
- Both instructions update automatically when the Host setup fields change.
- The join instruction explicitly requires asking the Hub admin for a one-time
  invite code first; without it, an agent must not join or create a Hub.

## Notes

- The invite field is optional: without a value the instruction uses a clear
  placeholder.
- Creating a Hub and joining a Hub are separate operations.
- Installer remains unsigned.
