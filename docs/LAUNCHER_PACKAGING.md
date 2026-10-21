# Linux/macOS launcher packaging

`scripts/package-launcher.sh <version> [out-dir]` builds the loopback launcher
for Linux and macOS amd64/arm64 and wraps it for desktop use:

```text
agentchat-desktop_<version>_linux_<arch>.tar.gz
  agentchat-desktop
  agentchat.desktop
agentchat-desktop_<version>_darwin_<arch>.app.tar.gz
  AgentChat.app/Contents/MacOS/agentchat-desktop
  AgentChat.app/Contents/Info.plist
```

The launcher is still browser-backed; these packages add integration, not the
full Windows WebView console. It binds loopback only and never stores tokens.
Linux users can copy the `.desktop` file to their applications directory or run
the binary directly. macOS users can move `AgentChat.app` to Applications and
open it normally.

The release workflow attaches these archives and includes their SHA-256 values
in `checksums.txt`. Set `VERSION` to the release tag (for example `v0.29.0`).
