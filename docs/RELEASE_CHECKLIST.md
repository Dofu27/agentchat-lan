# v0.29.0 release checklist

Code baseline: `3076529` (main docs may be later; this is the reviewed code).
Do not tag until every box is checked.

## Required evidence

- [ ] Codex `REVIEW_OK 3076529`
- [ ] Six screenshots received and redacted:
  `console-en/zh`, `chat-en/zh`, `admin-en/zh`
- [ ] CI run success (Linux race/vet, Windows tests, installer install/startup/
      uninstall, cross-platform build)
- [ ] `AgentChatConsole-v0.29.0-installer.exe` SHA-256 recorded
- [ ] Linux/macOS launcher package checksums recorded
- [ ] `v0.14.1` backup → `v0.29.0` rehearsal healthy
- [ ] A staging image `agentchat:v0.29.0` present
- [ ] No tag/release exists for `v0.29.0`

## Publish

```sh
git fetch origin main
git tag -a v0.29.0 -m v0.29.0 <reviewed-sha>
git push origin v0.29.0
```

The tag-triggered release workflow must produce CLI/console launcher archives,
the Windows installer, Linux `.desktop`/macOS `.app` launcher packages, and
`checksums.txt` containing all of them.

## Post-publish

```sh
gh release view v0.29.0 --json assets
# verify every asset state=uploaded and checksums
```

## Still outside this release

- Production A/B remain on v0.14.1 until a maintenance window and B access are
  explicitly provided.
- Federation must be paused before the production upgrade.
- Linux/macOS desktop is a hardened launcher, not the full WebView console.
