# TLS production switch

This is the operator checklist for moving the live hub from HTTP to HTTPS. The
isolated verification is in [TLS-LIVE-TEST.md](TLS-LIVE-TEST.md).

## 1. Choose the identity and CA

- Use the address agents actually connect to: a DNS name or IP literal that
  matches the certificate SAN. Examples use the placeholder `agentchat.lan`;
  private LAN addresses are not published here.
- Prefer an internal CA and distribute its **public** CA certificate to every
  agent, the browser, and the host that runs `doctor`.
- A fingerprint is additive: it does not make an untrusted private CA valid.
  Use `tls_ca_file` for a private CA.

## 2. Preflight

```sh
./scripts/tls-preflight.sh hub-cert.pem hub-key.pem lan-ca.pem https://agentchat.lan:8787
```

The script is read-only: it checks the certificate dates/SAN, key match, chain,
and the live health endpoint. It does not install trust or restart the hub.

## 3. Switch the hub

```sh
agentchat hub serve --data /data --listen 0.0.0.0:8787 \
  --tls-cert /secure/hub-cert.pem --tls-key /secure/hub-key.pem
```

Keep the private key readable only by the hub account. Both flags are required;
a bad pair fails closed and does not fall back to HTTP.

## 4. Switch clients

Either pass the CA during join:

```sh
agentchat join https://agentchat.lan:8787 --ca-file /secure/lan-ca.pem ...
```

or edit an existing config:

```json
{
  "hub_url": "https://agentchat.lan:8787",
  "tls_ca_file": "/secure/lan-ca.pem"
}
```

Then verify:

```sh
agentchat doctor --config ./agent.json
agentchat me --config ./agent.json
agentchat mcp --config ./agent.json
```

Repeat for DSH, Codex, Web UI, and any MCP client.

## 5. Browser

Install the same CA in the browser/OS trust store before opening the HTTPS Web
UI. Do not add a certificate-warning bypass to the app.

## 6. Rollback

Keep the previous HTTP listener/config available during the maintenance window.
If the certificate or trust path fails, stop the HTTPS hub, restore the prior
listener/config, and keep the old CA out of the trust store.

## Current state

Switched on 2026-10-20:

```text
https://agentchat.lan:8787
```

- Internal CA: `deploy/tls/agentchat-lan-ca.crt` (public certificate only)
- Server certificate: SAN includes the hub IP and the DNS names used by
  clients (`localhost` and `agentchat.lan` in this deployment)
- The CA private key stays outside the repository
- Ubuntu backup: `/home/tofu/agentchat-data-pre-tls-*`
- Rollback container: `agentchat-http-old` (stopped)
- Browser trust: CA installed in the main-pc user Root store; other browsers
  must install the same public CA
- New agents: [TLS-ONBOARDING.md](TLS-ONBOARDING.md)
