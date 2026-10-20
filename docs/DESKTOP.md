# Windows desktop console

`agentchat-desktop` is an independent Windows console for the local hub. It
uses an embedded Microsoft Edge WebView2 window, so it does not need a browser
tab and does not expose a public management port.

## Requirements

- Windows 10/11 with the Microsoft Edge WebView2 Runtime (normally preinstalled
  on Windows 11)
- Administrator rights to start/stop or change the startup type of the hub
  service
- A hub service already installed (for example `AgentChatFedB`)

## Configuration

Create `%APPDATA%\AgentChat\desktop.json` (or pass `--config`):

```json
{
  "service": "AgentChatFedB",
  "hub_url": "https://agentchat.lan:8788",
  "token_file": "C:/AgentChat/data/admin.token",
  "title": "AgentChat Console",
  "width": 1100,
  "height": 780
}
```

`token_file` is read locally and sent only to the configured hub. Keep the
desktop config and token file private.

Start it:

```powershell
AgentChatConsole.exe --config C:\AgentChat\desktop.json
```

The console binds an ephemeral loopback-only HTTP port for its own WebView2
page and closes it when the window closes. All `/api` requests require a
per-launch random capability injected by the native WebView; the HTML alone
does not grant access. Host/Origin and JSON content-type checks reject
cross-site and DNS-rebinding attempts. Only literal loopback listen addresses
are accepted, and the bound address is checked again after `listen`.
Hub admin requests refuse redirects and only attach the token to the configured
HTTPS origin; `tls_ca_file` can pin the internal CA.

## Controls

- Service state and PID
- Start, stop, restart
- **Start with Windows** switch: flips the service between Automatic and
  Manual, using the Windows service manager
- Hub health
- Federation node/peer/link/outbox status
- Latest 50 dead letters and retry

The console does not store the admin token in browser storage; it reads the
configured local file at startup. Passwords, secrets and payloads are not
rendered.

## Headless API (local diagnostics)

For supervised diagnostics only:

```powershell
AgentChatConsole.exe --config C:\AgentChat\desktop.json --headless --listen 127.0.0.1:18765
```

Then `GET /api/status` on that loopback address. This is not a replacement for
the normal admin UI and must not be exposed beyond loopback.
