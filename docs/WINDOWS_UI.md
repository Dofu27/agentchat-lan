# Windows UI startup, chrome, and icon

The console is `AgentChatConsole.exe`; the hub is the `AgentChatFedB` service.
Do not start the hub binary directly for the desktop UI.

## Startup

The installer creates a Startup shortcut to:

```text
%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\AgentChat Console.lnk
  target: <install-dir>\AgentChatConsole.exe --config <desktop.json>
  icon:   <install-dir>\AgentChatConsole.exe,0
```

The shortcut is preferred over `cmd.exe` wrappers: it avoids a visible console
window and does not re-parse `%`/quotes. `AgentChat ConsoleUI` is also registered
at logon as a fallback; the console's single-instance mutex prevents duplicates.

The Codex bridge loop (`AgentChatLoop`) is a separate background task. On
legacy hosts it may use a `.cmd`; wrap it in a hidden `wscript.exe` launcher so
it does not show a black cmd window.

## Custom chrome

The main console window is frameless (`WS_POPUP`, no native caption). The page
draws its own dark title bar with drag, minimize, maximize, and close controls;
the Go host binds `__winDrag`, `__winMin`, `__winMax`, and `__winClose`.

## Icon and tray

`assets/agentchat.ico` is embedded into the console and installer via committed
`rsrc_windows_amd64.syso` files:

```text
cmd/agentchat-desktop/rsrc_windows_amd64.syso
cmd/agentchat-installer/rsrc_windows_amd64.syso
```

Regenerate after changing the icon:

```bash
go install github.com/akavel/rsrc@latest
scripts/generate-windows-icons.sh
```

Shortcuts use the executable icon. If a shortcut was created by an older
installer, recreate it so `IconLocation` points at the new executable.
