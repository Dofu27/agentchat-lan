# Publishing to GitHub

The repository is configured for:

- owner: `Dofu27`
- repo: `agentchat-lan`
- module: `github.com/Dofu27/agentchat-lan`
- license: MIT (already in `LICENSE`)

The repository starts **private**. Make it public only after the M1/M2 review.

## 1. Module path (already done)

`go.mod` and imports already use `github.com/Dofu27/agentchat-lan`. If you fork
the project, run:

```bash
./scripts/rename-module.sh github.com/<your-owner>/agentchat-lan
gofmt -w . && go test ./...
```

## 2. Create the private repository

Status: the private repository exists and `main` was pushed on 2026-10-19.

On GitHub, create an empty **private** repository named `agentchat-lan`. Do not
initialize it with a README, license, or `.gitignore`; this repo already has
them.

Authenticate with GitHub CLI or an SSH key. Do not paste a personal access token
into chat, an issue, or a commit. Then:

```bash
git remote add origin git@github.com:Dofu27/agentchat-lan.git
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
docker build -f deploy/Dockerfile -t ghcr.io/dofu27/agentchat:0.1.0 .
docker push ghcr.io/dofu27/agentchat:0.1.0
```

Add a workflow later if you want automatic image releases.

## 7. Private review before public

Keep the GitHub repository private until these are true:

- [ ] `README.md` quick start works on a clean machine.
- [ ] No tokens, private IPs, or personal paths in git history.
- [ ] `LICENSE` and GitHub license detection agree.
- [ ] CI is green on `main`.
- [ ] The release draft contains all platform binaries.
- [ ] `docs/CONTRACT.md` matches the released behavior.
- [ ] Security policy is visible under the Security tab.
- [ ] DSH and Codex have completed the M1 acceptance matrix.
- [ ] No private team names or internal IPs remain in docs.

Then switch the repository to public and publish the first release.
