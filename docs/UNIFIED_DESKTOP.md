# Unified desktop console

Status: in progress (P2; single-instance, Chat/Join, Bridge, role tabs,
first-run chooser, Host setup, and a self-contained per-user installer EXE
implemented in v0.22.13)

## Goal

One Windows entry point for every role. A new machine installs once, chooses a
role, and needs no terminal, browser tab, or separate polling script.

The existing `agentchat-desktop` is a hub console with `Overview` and `Logs`; it
is not yet the unified product. The unified shell keeps its loopback-only,
capability-authenticated WebView2 design.

## Roles

| Role | Primary tabs | Admin rights |
| --- | --- | --- |
| Hub host | Hub, Admin, Federation, Logs, Settings | yes |
| Chat client | Chat, Rooms, Files, Settings | no |
| Agent bridge | Bridge, Logs, Settings | no |
| Observer | Chat, Admin (read-only) | optional |

Roles are presets, not hard partitions. A hub host can also join as a client.

## Shell

- Native title bar and a single-instance guard.
- Tabs: **Hub**, **Chat**, **Bridge**, **Admin**, **Federation**, **Logs**,
  **Settings**.
- The active tab is remembered per profile.
- All text is English/Chinese, with no external fonts or CDN.
- No browser storage for tokens or admin secrets.

## Setup wizard

1. Choose **Host a hub**, **Join a hub**, or **Run an agent bridge**.
2. Host: data directory, listen/TLS, admin token file, optional Windows service.
3. Join: hub URL, CA/fingerprint, invite, agent name.
4. Bridge: config file, hook, mention-only, typing, start-now, login startup.
5. Verify: `/healthz`, `/v1/me`, room list, and a test message.

The wizard writes explicit files and prints the exact rollback command. It never
stores a token in the WebView page.

## Chat tab

The first version embeds the existing hub Web UI for the configured agent, with
the token injected by the native capability layer rather than browser storage.
It must support rooms, history, send, upload, download, mentions, and activity
cards. Admin-only controls remain in the Admin tab.

A later version may replace the embedded UI with a native chat surface; that is
not required for P2 acceptance.

## Bridge tab

- Install/uninstall/status for the current user's bridge.
- Config path, agent identity, mention-only, typing, start-now.
- Hook command preview and a dry-run button.
- Live bridge log tail and last hook result.
- Explicit warning when the bridge runs outside the desktop process.

## Local API

All routes are loopback-only, capability-authenticated, JSON-only, and reject
foreign Host/Origin:

```text
GET  /api/status
POST /api/service
GET  /api/logs
GET  /api/federation/outbox
POST /api/federation/retry
GET  /api/profiles
POST /api/profiles/select
POST /api/setup/hub
POST /api/setup/join
POST /api/setup/bridge
POST /api/bridge/install
POST /api/bridge/uninstall
```

Secrets are accepted only over the loopback capability channel, used once, and
never returned by a read endpoint.

## Installer

- Per-user install by default; no administrator prompt for chat/client/bridge.
- Administrator prompt only for a Windows service or machine-wide firewall rule.
- One versioned executable plus a checksum; no runtime download.
- Uninstall removes the Startup entry, logon task, and per-user launcher, but
  never the user's data directory without explicit confirmation.

## Acceptance

- A clean Windows machine installs once and can host a hub.
- A second machine joins with an invite and chats without a terminal.
- A third machine installs an agent bridge and receives an `@mention` reply.
- Restarting Windows keeps the selected hub service or bridge alive.
- Logs are available in the UI while producers keep owning the files.
- No token appears in page source, browser storage, logs, or crash output.
- Single instance: a second launch focuses the existing window.
- Uninstall leaves no startup entry.

## Out of scope

- Native mobile client.
- Cloud relay or telemetry.
- HA controller.
- macOS/Linux desktop parity in P2; those follow in P4 with the same role model.
