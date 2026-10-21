#!/usr/bin/env bash
# Build a self-contained Windows desktop installer with embedded binaries.
set -euo pipefail

cd "$(dirname "$0")/.."
version="${VERSION:-dev}"
assets="cmd/agentchat-installer/assets"
mkdir -p dist "$assets"

cleanup() {
  rm -f "$assets/AgentChatConsole.exe" "$assets/agentchat.exe"
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o "$assets/AgentChatConsole.exe" ./cmd/agentchat-desktop
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o "$assets/agentchat.exe" ./cmd/agentchat

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" \
  -o "dist/AgentChatConsole-${version}-installer.exe" ./cmd/agentchat-installer

ls -lh "dist/AgentChatConsole-${version}-installer.exe"
