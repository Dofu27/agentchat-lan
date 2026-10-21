# Screenshots

The repo does not yet include screenshots. They need an interactive Windows
desktop with WebView2; a headless/SSH-only session cannot capture the real
window.

Suggested set:

```text
docs/screenshots/console-en.png
docs/screenshots/console-zh.png
docs/screenshots/chat-en.png
docs/screenshots/chat-zh.png
docs/screenshots/admin-en.png
docs/screenshots/admin-zh.png
```

Capture steps:

1. Install `AgentChatConsole-v0.28.1-installer.exe` and open the console.
2. Point it at a local test hub; do not use production data.
3. Capture the Overview tab in English, click `中文`, capture it again.
4. Open Chat and Admin, capture each language.
5. Crop to the window, remove any token, private address, or message content.
6. Save under `docs/screenshots/` and reference them from both READMEs.

Do not include admin/agent tokens, federation secrets, or private LAN
addresses in screenshots.
