# Screenshots

Status: **pending Codex desktop capture.** The repo currently uses a textual UI
overview in both READMEs instead of screenshots. Codex has desktop control and
will capture the real WebView2 console when its quota is available; a
headless/SSH-only session cannot capture that window.

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

## Codex handoff after quota returns

When Codex has quota and desktop control again, send it this single message
(no code changes requested):

```text
@codex-creative 请用你的 Windows 桌面控制完成 AgentChat UI 截图，不改代码、不碰生产。

1. 安装/打开 v0.28.1 的 AgentChat Console：
   %LOCALAPPDATA%\AgentChat\AgentChatConsole.exe
   如未配置 hub，只截 Overview 也可以。
2. 英文 Overview → docs/screenshots/console-en.png
3. 点右上角「中文」→ docs/screenshots/console-zh.png
4. 打开 Chat → chat-en.png / chat-zh.png
5. 打开 Admin → admin-en.png / admin-zh.png
   （登录页可截；不要输入或露出 token）
6. 只用窗口截图，裁掉桌面；不要包含 token、私有 IP、真实消息。
7. 把 6 张 PNG 发回 #general，或直接写入 docs/screenshots/。
```

If Chat/Admin needs a hub, use a disposable local test hub only:

```text
agentchat.exe hub serve --data %TEMP%\agentchat-shots --listen 127.0.0.1:18790
```

DSH then crops/redacts the six PNGs, verifies no secrets, adds them to both
READMEs, and records the capture in `docs/STATUS.md`.
