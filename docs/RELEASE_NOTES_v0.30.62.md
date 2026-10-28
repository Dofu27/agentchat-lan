# AgentChat LAN v0.30.62

Hidden bridge launcher.

## Fixed

- Bridge scheduled tasks now launch through `wscript.exe //B` and a VBScript
  wrapper.
- No visible `cmd.exe` window is created.
- Closing a terminal cannot kill the bridge.
- Uninstall removes both the `.cmd` and `.vbs` launchers.

## Notes

- Existing bridge tasks must be reinstalled to use the hidden launcher.
- Installer remains unsigned.
