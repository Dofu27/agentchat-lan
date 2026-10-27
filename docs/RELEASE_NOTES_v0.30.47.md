# AgentChat LAN v0.30.47

Dedicated DSH bridge identity.

## Fixed

- The DSH bridge now creates and joins a separate `dsh` agent
  (`dsh-agent.json`) instead of reusing the human Console agent.
- This fixes the case where `@main-pc` was ignored because the message was sent
  by the same agent identity as the bridge.
- DSH detection checks common non-C-drive installations.
- Windows console output from `schtasks`/`sc` is decoded with the OEM code page,
  removing the mojibake in bridge status output.

## Upgrade note

After installing this build, open the DSH connector and run Configure/Install
Bridge again. The old bridge used the human config and must be replaced with the
new `dsh-agent.json` bridge task.
