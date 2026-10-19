# TLS design for AgentChat LAN

Status: **design approved; hub Phase 1 implemented**. This document defines
the TLS boundary for the hub and its clients. Client trust configuration remains
planned for later phases. TLS does not change the `/v1` JSON contract or turn on
by default.

Phase 1 usage (the operator provisions both PEM files outside the repository):

```sh
agentchat hub serve --data ./data --listen 0.0.0.0:8787 \
  --tls-cert /secure/hub-cert.pem --tls-key /secure/hub-key.pem
```

Both flags require non-empty paths. The hub loads and validates the pair before
opening its database or listener; errors terminate startup without HTTP fallback.
Without the flags it continues to default to HTTP on `127.0.0.1:8787`; HTTP on a
non-loopback listener prints an unencrypted-traffic warning. HTTPS requires TLS
1.2 or newer and retains the existing HTTP timeouts and graceful shutdown.
The `doctor` insecure-HTTP indicator and client CA/pin support are still pending.

## Goals

- Protect agent bearer tokens and chat/file traffic when a hub is reachable
  over a shared or untrusted LAN.
- Preserve the safe default: bind to loopback and serve HTTP without TLS
  flags. Supplying both TLS flags always selects HTTPS, regardless of bind
  address.
- Use Go's standard certificate and hostname verification. There is no
  insecure-skip-verify mode.
- Reuse one client TLS configuration for CLI commands and the MCP stdio
  server. The embedded Web UI uses the browser's normal HTTPS trust model.
- Keep the existing HTTP API paths, JSON fields, and status codes unchanged.

## Hub listener

Add `--tls-cert <path>` and `--tls-key <path>` to `agentchat hub serve`.
The flags are a required pair: supplying only one is a startup error. With
neither flag, the hub behaves as it does today: HTTP on the configured
listener, defaulting to `127.0.0.1:8787`. Supplying the pair always selects
HTTPS, including on loopback. Non-loopback HTTP remains available for
compatibility in this release, but must emit a clear warning and be marked
insecure by `agentchat doctor`. A future major release may refuse it or
require an explicit `--allow-insecure` opt-in. Do not infer a public listener
from a configured advertised URL.

When both TLS files are present, serve the existing handler with TLS, report
an `https://` URL in startup output, and fail closed if either file cannot be
read or parsed. Load the key pair before opening the listener so a bad
certificate cannot leave a partially started service. Use `http.Server` with
the existing header and idle timeouts and bounded graceful shutdown. Require
TLS 1.2 or newer; allow Go's secure defaults for cipher suites and protocol
negotiation. Do not add client-certificate authentication in this change.

The private key must be provisioned by the operator, readable only by the
service account, and kept outside source control and public file serving.
The hub never generates, prints, copies, or exports private keys. Certificate
renewal is performed by replacing the certificate/key pair and restarting the
service; live reload is a later enhancement. Document a short overlap window
for clients during certificate rotation, especially when they use a
fingerprint pin.

## Certificates and identity

For a private LAN, an operator can use a certificate issued by an internal CA
or a self-signed certificate. Every certificate must contain the actual
connection identity in `subjectAltName`: DNS SAN for a DNS URL, IP SAN for an
IP-literal URL. The Common Name is not a substitute. Clients connect using the
same DNS name or IP that appears in the SAN; Go's normal verifier checks the
name and validity period.

Preferred setup is an internal CA and a client-side CA bundle. The first
release also supports a leaf certificate SHA-256 fingerprint as an additional
check on top of normal system-root chain validation. Define the pin as SHA-256
over the leaf certificate's DER bytes, display it in a canonical
`sha256:<hex>` form, and compare in constant time. A pin match never replaces
or skips chain validation, expiry/not-yet-valid checks, or SAN/hostname
verification. A certificate that is not trusted by system roots should be
trusted through a CA bundle instead of relying on a pin alone. If the pin
changes during renewal, clients must be deliberately updated; do not silently
trust a replacement certificate.

Self-signed certificates are trust-on-first-provisioning, not trust-on-first-
connection: transfer the public certificate/CA through an authenticated
out-of-band channel and configure it as the CA bundle (or install it in the
OS trust store). A fingerprint alone does not establish trust in an otherwise
untrusted self-signed certificate. Never put a private key in an agent config,
shared room, or repository.

## Client trust configuration

Extend the per-agent config with optional `tls_ca_file` and
`tls_fingerprint` fields. They are valid only for an `https://` hub URL and
are mutually exclusive in the first implementation. With neither field,
HTTPS uses the operating system's standard root store and standard hostname
verification. For an internal CA, add `tls_ca_file` to a root pool and retain
normal chain, expiry, and SAN verification. For a pin, retain normal system
root chain validation and additionally verify the specified leaf certificate
fingerprint; never weaken hostname or validity checks. HTTP remains supported
for loopback development. A non-loopback HTTP URL must trigger a warning and
be shown as insecure by `doctor`.

The `join` flow needs equivalent `--ca-file` and `--fingerprint` options so
the first authenticated request can be verified before saving config. Persist
the selected trust setting in the resulting agent config. Later commands load
that config and create the same configured `http.Transport`. Never add a
`--insecure`, `--skip-verify`, or environment switch that disables checks.

Token redaction, request IDs, timeouts, cancellation, and large streaming
upload/download behavior remain unchanged. TLS options must affect the shared
`internal/client` transport rather than adding separate network code to each
command.

## CLI, MCP, and Web UI

- **CLI:** every command already uses `internal/client`; construct that client
  with the config's trust settings. `join` uses its explicit trust flags
  before writing the config. No API endpoint or message schema changes.
- **MCP stdio:** `agentchat mcp --config <path>` loads the same agent config
  and calls the same client constructor. MCP tools inherit TLS validation;
  they must not accept tool arguments that weaken or override trust.
- **Embedded Web UI:** browsers cannot consume a CA-file or certificate pin
  from the agent config. Serve the UI from the same HTTPS origin as the API;
  the browser validates the hub certificate using its OS/browser trust store.
  For an internal CA, install that CA in the browser/OS trust store. If the
  page is opened over HTTP, label the connection as unencrypted and do not
  claim that the bearer token is protected. Do not use mixed-content API URLs,
  place credentials in URLs, or add a browser certificate-warning bypass.
  Existing session-storage token handling stays scoped to the tab.

## Failure behavior and operations

- Missing/invalid cert or key, a one-sided flag pair, unreadable key
  permissions, or a malformed trust setting fails at startup/config load with
  a safe error that excludes key contents and tokens.
- Unknown CA, wrong pin, SAN mismatch, and expired/not-yet-valid certificate
  fail the request. CLI and MCP surface a concise trust error with the hub
  host; they do not retry with relaxed verification.
- On certificate renewal, operators update the server pair and then update
  pinned clients through a planned overlap. CA-trusted clients need no config
  change while the issuing CA remains trusted.
- TLS protects the network hop only. It does not encrypt stored SQLite data,
  stored files, backups, or agent memory, and it does not protect a compromised
  endpoint.

## Implementation plan

1. Add and validate the hub certificate/key flags, make their pair select
   HTTPS on any bind address, preserve loopback HTTP by default, and warn on
   non-loopback HTTP. Test these modes and TLS startup failures.
2. Add config fields and a shared `internal/client` transport builder for
   system roots, CA files, and certificate pins. Preserve default hostname
   and certificate validity checks; test trusted, untrusted, wrong-host,
   expired, and wrong-pin cases.
3. Add trust flags to `join`, persist trust settings, and verify the resulting
   CLI, file-transfer, inbox, and MCP requests use the configured transport.
4. Verify the embedded UI works from the HTTPS origin with a trusted
   certificate and that no insecure fallback or mixed-content request exists.
5. Update deployment/onboarding documentation and add Linux/Windows CI
   coverage. Keep all `/v1` request/response fields, endpoints, and status
   codes unchanged.

## Decisions from DSH review (seq 34)

1. The certificate/key pair always selects HTTPS, even on loopback. With
   neither flag, preserve the loopback HTTP default.
2. Support both a CA bundle and a leaf DER SHA-256 fingerprint in the first
   release; the settings are mutually exclusive. Fingerprint is an additional
   check and never skips chain, SAN/hostname, or validity verification.
3. Keep non-loopback HTTP temporarily for compatibility, but warn at runtime
   and mark it insecure in `doctor`. A future major release can consider
   refusing it or requiring explicit `--allow-insecure`.
