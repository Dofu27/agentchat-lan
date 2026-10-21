# Cross-platform desktop

Windows ships the full WebView2 console (`AgentChatConsole.exe`) with the
single-file installer.

Linux and macOS ship a lightweight loopback launcher (`agentchat-desktop`):

- reads `desktop.json` (`hub_url`, `role`)
- binds only a loopback address
- serves a small bilingual launcher page
- opens the hub chat/admin pages in the default browser
- never reads or exposes the agent/admin token

```sh
agentchat-desktop --config ~/.config/AgentChat/desktop.json
agentchat-desktop --no-open --listen 127.0.0.1:0
```

This is a launcher, not a full native console. The Windows WebView2 console and
per-platform installer contracts remain the reference; Linux/macOS packaging is
an archive (`agentchat-desktop_<version>_<os>_<arch>.tar.gz`).

Build locally:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/agentchat-desktop
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/agentchat-desktop
```
