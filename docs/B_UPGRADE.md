# B (windows-host-b) upgrade plan

B is Windows 10, reached from operator-workstation as `operator@windows-host-b` (key-only). It runs
the hub as an auto-start **LocalSystem** service `AgentChatFedB` from a
user-writable directory. The v0.29.0 service trust check therefore requires the
new binary to move to `C:\Program Files\AgentChat\agentchat.exe` before the
service is reconfigured.

## Current production shape

```text
service:  AgentChatFedB (LocalSystem, auto)
binary:   %USERPROFILE%\Documents\Codex\agentchat-v0100-svc.exe  (dev)
data:     %USERPROFILE%\Documents\Codex\agentchat-hub-b
TLS:      %USERPROFILE%\Documents\Codex\creative-server.{crt,key}
federation: %USERPROFILE%\Documents\Codex\agentchat-hub-b\federation.json
backup:   scheduled task AgentChatFedBBackup, daily 03:10
```

A read-only SSH session is elevated (`net session` succeeds).

## Preflight evidence

- B `/healthz`: `status=ok`, `version=dev`; `/readyz`: ready.
- v0.29.0 Windows CLI staged as `agentchat-v0290-stage.exe`; `version` prints
  `agentchat 0.29.0`; `hub backup` on B data succeeded (463,394 bytes).
- The same binary is staged at `C:\Program Files\AgentChat\agentchat.exe`
  (SHA-256 `b1321000804e53ebbe22f39164656cf47c94f78d8947d2d16fbb4a1944949fa6`);
  the running service still uses the old `agentchat-v0100-svc.exe`.
- Backup copied to the operator backup directory with SHA-256
  `09696b6870a1beaa445ef60f1c6d63b7f1e1c07d2da560903812ee22365a1caf`.
- Restored that backup with v0.29.0 on a loopback test hub: health
  `{"status":"ok","version":"0.29.0"}`, ready.
- A v0.29.0 image was also run against copied A data/TLS: health/ready.

## Window sequence

1. Pause federation traffic (stop/upgrade B before A; do not edit link config).
2. Create a fresh B backup with the current binary.
3. Copy `agentchat-v0290-stage.exe` to
   `C:\Program Files\AgentChat\agentchat.exe`; verify SHA-256.
4. `sc.exe stop AgentChatFedB`, wait for `STOPPED`.
5. `sc.exe config AgentChatFedB binPath= "\"C:\Program Files\AgentChat\agentchat.exe\" service run --name AgentChatFedB --log \"<data>\service.log\" -- hub serve --data \"<data>\" --listen 0.0.0.0:8788 --tls-cert \"<crt>\" --tls-key \"<key>\" --federation-config \"<data>\federation.json\""`
6. Start the service; verify `/healthz` version 0.29.0, `/readyz`, agents,
   messages, files, and federation status.
7. On failure: stop service, restore old binPath, start old binary, keep data.

B's data/TLS paths are already local and readable by LocalSystem; do not copy
tokens or TLS keys into the repo or chat.
