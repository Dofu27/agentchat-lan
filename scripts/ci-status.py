#!/usr/bin/env python3
"""Read-only GitHub Actions/PR status for the private review.

Token sources, in order:
  --token-file, GITHUB_TOKEN, ~/.config/agentchat/github-token

The token is never printed. Use a fine-grained token with:
  Actions: read, Contents: read, Pull requests: read, Metadata: read
"""
import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

DEFAULT_REPO = "Dofu27/agentchat-lan"
DEFAULT_TOKEN_FILE = Path.home() / ".config" / "agentchat" / "github-token"


def load_token(path: str | None) -> str:
    if path:
        return Path(path).read_text(encoding="utf-8").strip()
    env = os.environ.get("GITHUB_TOKEN", "").strip()
    if env:
        return env
    if DEFAULT_TOKEN_FILE.exists():
        return DEFAULT_TOKEN_FILE.read_text(encoding="utf-8").strip()
    raise SystemExit(
        "no GitHub token: pass --token-file, set GITHUB_TOKEN, or create "
        f"{DEFAULT_TOKEN_FILE} with mode 0600"
    )


def api(repo: str, token: str, path: str) -> dict | list:
    req = urllib.request.Request(
        f"https://api.github.com/repos/{repo}{path}",
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {token}",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "agentchat-ci-status",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")[:500]
        raise SystemExit(f"GitHub API {exc.code} for {path}: {body}") from exc
    except urllib.error.URLError as exc:
        raise SystemExit(f"GitHub API connection error: {exc.reason}") from exc


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", default=DEFAULT_REPO)
    parser.add_argument("--token-file")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    token = load_token(args.token_file)
    if not token:
        raise SystemExit("empty token")

    repo = api(args.repo, token, "")
    runs = api(args.repo, token, "/actions/runs?per_page=8")
    pulls = api(args.repo, token, "/pulls?state=open&per_page=20")

    latest = []
    for run in runs.get("workflow_runs", [])[:8]:
        latest.append(
            {
                "workflow": run.get("name"),
                "status": run.get("status"),
                "conclusion": run.get("conclusion"),
                "branch": run.get("head_branch"),
                "sha": (run.get("head_sha") or "")[:8],
                "url": run.get("html_url"),
            }
        )

    prs = []
    for pr in pulls:
        prs.append(
            {
                "number": pr.get("number"),
                "title": pr.get("title"),
                "draft": pr.get("draft"),
                "mergeable": pr.get("mergeable"),
                "url": pr.get("html_url"),
            }
        )

    result = {
        "repo": args.repo,
        "private": repo.get("private"),
        "default_branch": repo.get("default_branch"),
        "runs": latest,
        "open_pulls": prs,
    }
    if args.json:
        print(json.dumps(result, indent=2))
    else:
        print(f"repo: {args.repo} private={result['private']} default={result['default_branch']}")
        print("workflow runs:")
        for run in latest:
            print(
                f"  {run['workflow']}: {run['status']} / {run['conclusion']} "
                f"branch={run['branch']} sha={run['sha']} {run['url']}"
            )
        print("open pull requests:")
        for pr in prs:
            print(
                f"  #{pr['number']} {pr['title']} draft={pr['draft']} "
                f"mergeable={pr['mergeable']} {pr['url']}"
            )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
