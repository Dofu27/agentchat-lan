# Publishing to GitHub

The repository is ready to publish, but the module path and clone URLs still use
the placeholder `github.com/agentchat-lan/agentchat`. Pick a GitHub owner and
repository name first.

Recommended:

- owner: your GitHub user or a small org
- repo: `agentchat-lan`
- module: `github.com/<owner>/agentchat-lan`
- license: MIT (already in `LICENSE`)

## 1. Rename the module

From the repository root:

```bash
./scripts/rename-module.sh github.com/<owner>/agentchat-lan
```

This updates `go.mod`, Go imports, and README clone URLs. Review the diff:

```bash
git diff
go test ./...
```

## 2. Create the repository

On GitHub, create an empty repository named `agentchat-lan`. Do not initialize
it with a README, license, or `.gitignore`; this repo already has them.

Authenticate with GitHub CLI or an SSH key. Do not paste a personal access token
into chat, an issue, or a commit. Then:

```bash
git remote add origin git@github.com:<owner>/agentchat-lan.git
git push -u origin main
```

## 3. Repository settings

- Description: `Self-hosted LAN chat and file transfer for AI agents. MCP + CLI, SQLite, one Go binary.`
- Topics: `ai-agents`, `agent`, `mcp`, `lan`, `self-hosted`, `golang`, `chat`, `file-transfer`
- Enable Issues and Discussions.
- Enable private vulnerability reporting (Security → Advisories).
- Protect `main`: require a pull request and passing CI.
- Disable wikis unless you plan to use them; docs live in `docs/`.

## 4. Verify CI

The first push runs `.github/workflows/ci.yml`:

- `gofmt`, `go vet`, `go test -race`
- cross-builds for Linux, Windows, and macOS

Fix a red build before tagging a release.

## 5. First release

```bash
git tag -a v0.1.0 -m "v0.1.0: M0 scaffold"
git push origin v0.1.0
```

`.github/workflows/release.yml` runs GoReleaser and creates a draft GitHub
release with:

- `agentchat-linux-amd64`
- `agentchat-linux-arm64`
- `agentchat-windows-amd64.exe`
- `agentchat-darwin-amd64`
- `agentchat-darwin-arm64`
- `checksums.txt`

Review the draft, then publish it.

## 6. Optional container image

The Dockerfile is in `deploy/Dockerfile`. To publish to GHCR:

```bash
docker build -f deploy/Dockerfile -t ghcr.io/<owner>/agentchat:0.1.0 .
docker push ghcr.io/<owner>/agentchat:0.1.0
```

Add a workflow later if you want automatic image releases.

## 7. Before announcing

- [ ] `README.md` quick start works on a clean machine.
- [ ] No tokens, private IPs, or personal paths in git history.
- [ ] `LICENSE` and GitHub license detection agree.
- [ ] CI is green on `main`.
- [ ] The release draft contains all platform binaries.
- [ ] `docs/CONTRACT.md` matches the released behavior.
- [ ] Security policy is visible under the Security tab.

## Placeholder checklist

These strings still need a real owner before publishing:

```bash
grep -R "agentchat-lan/agentchat" -n .
grep -R "your-org" -n .
```

Run the rename script, then search again.
