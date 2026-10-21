# Codex review packet

Use this once for the final review; do not re-review older SHAs.

```text
Repository: github.com/Dofu27/agentchat-lan
Baseline:   main 3fcde25
Branch:     codex/security-4577c38 was merged earlier
Tag:        none for 3fcde25
```

Fetch `origin main`; the branch is public. Review only `3fcde25`.

## Scope

1. P3 federation read receipts: opt-in, default off, peer-authenticated,
   linked-room-only, monotonic/capped, additive `federation_receipts`, bounded
   best-effort forwarding, no ACK change, receiver never re-forwards.
2. P4 multi-peer DM: per-peer conversations/outboxes, no cross-peer payload
   reuse, unknown-peer config rejected.
3. P4 multi-peer typing: per-peer ephemeral sessions/rate buckets; same room
   can receive several peers; no remote re-forwarding.
4. Launcher hardening: HTTP(S)-only/no-userinfo URL, normalized path, role
   guidance; launcher remains a browser launcher, not the WebView console.
5. Desktop Join: `O_EXCL` agent config save, concurrent join cannot overwrite.
6. Installer: embedded CLI version is `v0.29.0`, not `dev`.
7. Security branch regressions: service trust, Job fail-closed, token bootstrap.

## Local evidence (DSH, not Codex)

```text
go test -race ./...       PASS
go vet ./...              PASS
Windows build             PASS
Linux launcher build      PASS
macOS launcher build      PASS
v0.14.1 restore rehearsal PASS (health 0.29.0, ready)
installer install         PASS, agentchat v0.29.0
```

## Known limits

- Linux/macOS ship a hardened launcher, not the full WebView console.
- P1 production A/B still run v0.14.1; no maintenance window/B access yet.
- Screenshots need Codex desktop capture.
- Forwarded read receipts and typing are advisory/best-effort; no durable
  outbox/retry by design.

## Requested output

```text
REVIEW_OK 3fcde25
```

or:

```text
BLOCKED: <P1/P2> <file:line> <minimal repro/test>
```

No implementation changes in this review.

