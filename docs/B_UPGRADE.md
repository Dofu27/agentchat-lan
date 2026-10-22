# B (windows-host-b) upgrade record

B is Windows 10, reached from operator-workstation as `operator@windows-host-b` (key-only). On
2026-10-22 13:05 Asia/Hong_Kong the service was moved from the user-writable
`agentchat-v0100-svc.exe` to the protected
`C:\Program Files\AgentChat\agentchat.exe` and restarted as v0.29.0.

## Original production shape (pre-cutover)

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
  (SHA-256 `b1321000804e53ebbe22f39164656cf47c94f78d8947d2d16fbb4a1944949fa6`).
- Backup copied to the operator backup directory with SHA-256
  `09696b6870a1beaa445ef60f1c6d63b7f1e1c07d2da560903812ee22365a1caf`.
- Restored that backup with v0.29.0 on a loopback test hub: health
  `{"status":"ok","version":"0.29.0"}`, ready.
- A v0.29.0 image was also run against copied A data/TLS: health/ready.

## Post-cutover

```text
service:  AgentChatFedB (LocalSystem, auto, Running)
binary:   C:\Program Files\AgentChat\agentchat.exe  (0.29.0, tag caa7444)
SHA-256:  2433557196994b666dc741814e242f18dd1dbadeb0a28904d2fcec0262869f83
health:   {"status":"ok","version":"0.29.0"} / ready
fresh backup: cutover-B-20261022-1304.tar.gz (469,797 bytes)
pre-final rollback: %USERPROFILE%\Documents\Codex\agentchat-v0290-prefinal.exe
federation: delivered=2, dead=0
```

The first B start used the earlier staging build; it was immediately replaced
with the tag-built binary so production matches the reviewed/released code.

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
