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
  "height": 780,
  "agentchat_path": "C:/AgentChat/agentchat.exe",
  "agent_config": "C:/AgentChat/agent-dsh.json",
  "bridge_name": "AgentChatBridge-dsh-main",
  "bridge_exec": "C:/AgentChat/dsh-hook.cmd",
  "role": "hub"
}
```

`agentchat_path` defaults to `agentchat.exe` beside the console. The optional
bridge fields enable the Bridge tab; they never contain agent tokens.

`token_file` is read locally and sent only to the configured hub. Keep the
desktop config and token file private.

Start it:

```powershell
AgentChatConsole.exe --config C:\AgentChat\desktop.json
```

## Single EXE installer

`AgentChatConsole-v0.22.8-installer.exe` embeds the console and CLI. It installs
per-user into `%LOCALAPPDATA%\AgentChat`, writes a first-run `desktop.json`, and
creates Start Menu/Startup entries:

```powershell
.\AgentChatConsole-v0.22.8-installer.exe -role hub
.\AgentChatConsole-v0.22.8-installer.exe -role client -hub https://agentchat.lan:8787
```

Useful flags: `-install-dir`, `-no-startup`, `-no-launch`, `-uninstall`, and
`-remove-data`. No administrator rights are needed. Uninstall requires the
`.agentchat-install.json` ownership marker written by this installer; it removes
only files listed there and refuses arbitrary directories. If a host policy blocks the
user Startup folder, the installer tries an HKCU Run entry; if both are blocked
it warns and still installs the binaries/config with exit code 0.

On first launch with no role chosen, the Overview tab shows a Hub / Join /
Bridge chooser. The selection is persisted as `setup_done` so later role changes
in Settings do not repeat it.

The earlier portable zip (`AgentChatConsole-v0.22.7-win64.zip`) remains available
for users who prefer explicit script-based installation.

The console binds an ephemeral loopback-only HTTP port for its own WebView2
page and closes it when the window closes. All `/api` requests require a
per-launch random capability injected by the native WebView; the HTML alone
does not grant access. Host/Origin and JSON content-type checks reject
cross-site and DNS-rebinding attempts. Listen addresses must resolve to a
loopback address (`127.0.0.1`, `::1`, or `localhost`), and the bound address is
checked again after `listen`.
Hub admin requests refuse redirects and only attach the token to the configured
HTTPS origin; `tls_ca_file` can pin the internal CA.

## Controls

- **Open Web admin**: opens the hub admin UI in a second embedded WebView2
  window and auto-fills the configured admin token on the configured origin
- **Host setup**: initializes a hub data directory and admin token, optionally
  with TLS cert/key/CA, updates the desktop config, and prints the `sc.exe`
  service-install command. It rebuilds the desktop HTTP client with the new CA,
  so a TLS hub works without a console restart. It never prints the admin token. The service panel's
  **Install hub service** button runs the same install and starts the service;
  it needs an elevated console.
- **Settings tab**: changes the console role (`hub`, `client`, `bridge`,
  `observer`) and persists it to `desktop.json`. Role changes hide unrelated
  tabs immediately.
- **Chat tab**: shows the configured agent identity and opens the hub Web UI in
  an embedded WebView2 window. The token is injected only when the page origin
  matches the configured hub. With no agent config, the tab offers an invite
  join form; the issued token is saved only to the user-only config file.
- **Bridge tab**: shows the configured agent bridge name/config/hook/binary and
  runs `agentchat bridge status|install|uninstall` with explicit arguments.
  Install uses mention-only, typing, and start-now defaults.
- **Logs tab**: tails local Desktop/Hub/Bridge JSONL or plain log files with a
  1-second refresh, pause/resume and source switch. `log_files` in `desktop.json`
  accepts either a plain file or a directory of `component-*.jsonl` segments;
  relative paths resolve against the config dir. Logs stay file-backed while the
  window is closed.
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
