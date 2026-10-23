#!/usr/bin/env bash
# Regenerate Windows icon/version/manifest resources before a release.
# Requires: go install github.com/tc-hib/go-winres@latest
set -euo pipefail

cd "$(dirname "$0")/.."
command -v go-winres >/dev/null || {
  echo "go-winres not found; run: go install github.com/tc-hib/go-winres@latest" >&2
  exit 1
}

go-winres make --in winres/desktop.json --out cmd/agentchat-desktop/rsrc --arch amd64
go-winres make --in winres/installer.json --out cmd/agentchat-installer/rsrc --arch amd64
go-winres make --in winres/cli.json --out cmd/agentchat/rsrc --arch amd64

echo "Regenerated Windows resources."
