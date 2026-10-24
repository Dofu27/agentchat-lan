#!/usr/bin/env bash
# Build a self-contained Windows desktop installer with embedded binaries.
set -euo pipefail

cd "$(dirname "$0")/.."
version="${VERSION:-dev}"
assets="cmd/agentchat-installer/assets"
mkdir -p dist "$assets"

webview2="$assets/MicrosoftEdgeWebView2RuntimeInstallerX64.exe"
webview2_sha="04b9f08d839c8c06f34a85acea0d9f1568d3d8aa309a77619aaa46bb29ade0f8"
if [ ! -f "$webview2" ]; then
  curl -L --fail --retry 3 \
    -o "$webview2" \
    "https://msedge.sf.dl.delivery.mp.microsoft.com/filestreamingservice/files/f3274495-ff02-440e-b522-2d4129a911e8/MicrosoftEdgeWebView2RuntimeInstallerX64.exe"
fi
printf '%s  %s\n' "$webview2_sha" "$webview2" | sha256sum -c -

cleanup() {
  rm -f "$assets/AgentChatConsole.exe" "$assets/agentchat.exe"
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "$assets/agentchat.exe" ./cmd/agentchat
cli_sha256="$(sha256sum "$assets/agentchat.exe" | awk '{print $1}')"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w -H windowsgui -X main.expectedCLISHA256=${cli_sha256}" -o "$assets/AgentChatConsole.exe" ./cmd/agentchat-desktop

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" \
  -o "dist/AgentChatConsole-${version}-installer.exe" ./cmd/agentchat-installer

ls -lh "dist/AgentChatConsole-${version}-installer.exe"
