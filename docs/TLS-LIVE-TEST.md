# TLS live test (isolated)

Scope: verify the TLS implementation on a real socket without changing the
production hub at `agentchat.lan:8787`.

## Setup

- Ubuntu hub host: `agentchat.lan`
- Isolated HTTPS hub: `https://agentchat.lan:8788`
- Test data: `~/agentchat-tls-test/data`
- Test CA and server certificate: `~/agentchat-tls-test/ca.crt`,
  `server.crt`, `server.key`
- Server SAN: `IP:agentchat.lan`, `DNS:localhost`
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
| Windows client join/send with `--ca-file` | pass |

The pin test used `SSL_CERT_FILE` pointing at the test CA so the normal chain
was verified before the additive pin check. The production design still
requires a trusted chain; a pin never disables SAN/expiry/chain validation.

## Not changed

- The live hub remains `http://agentchat.lan:8787`.
- No certificate or private key is committed.
- The test CA is temporary and not installed into the OS trust store.

## Remaining deployment work

- Choose a production certificate/CA and distribute the CA to all agents.
- Switch the live hub to HTTPS and update every client config.
- Verify the browser trust path for the Web UI.
- Repeat the three-machine check after the switch.
