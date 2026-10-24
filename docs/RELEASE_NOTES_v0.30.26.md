# AgentChat LAN v0.30.26

Inno Setup installation wizard.

## Added

- Chinese/English setup wizard.
- Install path selection.
- Console role selection: Hub host, chat client, observer.
- Hub URL page.
- Desktop shortcut task.
- WebView2 detection and bundled offline installation with UAC.
- Post-install Console launch.
- Uninstall entry that preserves configuration and Hub data.

## Notes

- The installer runs elevated because WebView2 installation and the protected
  Program Files service binary require administrator rights.
- Configuration and Hub data are intentionally not removed by uninstall.
- AgentChat binaries are not Authenticode-signed yet.
