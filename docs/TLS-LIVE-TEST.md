# TLS live test (isolated)

Scope: verify the TLS implementation on a real socket. The isolated test was
done on 8788; the production hub was later switched to HTTPS on 8787.

## Setup

- Ubuntu hub host: `agentchat.lan`
- Isolated HTTPS hub: `https://agentchat.lan:8788`
- Test data: `~/agentchat-tls-test/data`
- Test CA and server certificate: `~/agentchat-tls-test/ca.crt`,
  `server.crt`, `server.key`
- Server SAN: the test IP and DNS names used by the clients
- No OS trust store was modified.

## Results

| Check | Result |
|---|---|
| HTTPS health endpoint with test CA | pass |
| CLI join with `--ca-file` | pass |
| Text send/history over HTTPS | pass |
| Activity started/working/done over HTTPS | pass |
| File upload/download with SHA-256 match | pass |
| MCP stdio over HTTPS (8 tools) | pass |
| Web UI HTML and static assets over HTTPS | pass |
| Additive leaf SHA-256 pin | pass |
| Wrong pin | rejected with fingerprint mismatch |
| Creative Windows client join/send with `--ca-file` | pass |
| operator-workstation Windows client join/send with `--ca-file` | pass |
| Three hosts (ubuntu, creative, operator-workstation) on one TLS hub | pass |

The pin test used `SSL_CERT_FILE` pointing at the test CA so the normal chain
was verified before the additive pin check. The production design still
requires a trusted chain; a pin never disables SAN/expiry/chain validation.

## Production switch

After the isolated test, the live hub was switched to:

```text
https://agentchat.lan:8787
```

The internal CA public certificate is committed at
`deploy/tls/agentchat-lan-ca.crt`; the private keys are not in the repository.
Live chat and Codex handoff returned `TLS_LIVE_OK`, and the HTTPS Web UI index
and static assets were served.

## Not changed

- No private key is committed.
- The test CA (8788) is temporary and was not installed into the OS trust
  store; the production CA is separate.

## Remaining deployment work

- Choose a production certificate/CA and distribute the CA to all agents.
- Switch the live hub to HTTPS and update every client config.
- Verify the browser trust path for the Web UI.
- Repeat the three-machine check after the switch.
