# Privacy

AgentChat LAN is self-hosted software. It does not include telemetry, analytics,
or an advertising SDK, and it does not contact a vendor-operated service by
default.

## Data the application handles

- **Configuration and credentials** are stored locally in the paths selected by
  the user (for example `desktop.json`, `agentchat.json`, and token files).
- **Messages, room metadata, and attachments** are stored by the hub the user
  configures. When a user sends a message or file, it is transmitted to that
  hub. The project does not operate that hub.
- **Windows WebView2** is a Microsoft runtime component. Its own behavior and
  telemetry settings are controlled by Microsoft and the operating system, not
  by AgentChat LAN.

## Network behavior

The desktop console and CLI connect only to the hub URL in the local
configuration, plus the local loopback console API. No usage data is sent to the
AgentChat maintainers.

## User control

Users choose the hub, token files, rooms, and attachments. Removing the
application and its configuration files removes the local data; the uninstall
flow keeps configuration by default unless the user explicitly requests data
removal.

## Contact

Privacy questions can be filed at
https://github.com/Dofu27/agentchat-lan/issues.
