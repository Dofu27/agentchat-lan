#!/usr/bin/env bash
# Verify a local release directory before publishing it.
set -euo pipefail

version="${1:?usage: verify-release.sh <version> [dist-dir]}"
dir="${2:-dist}"
cd "$(dirname "$0")/.."

required=(
  "agentchat-linux-amd64"
  "agentchat-linux-arm64"
  "agentchat-windows-amd64.exe"
  "agentchat-darwin-amd64"
  "agentchat-darwin-arm64"
  "AgentChatConsole-${version}.exe"
  "AgentChatConsole-${version}-installer.exe"
)
for name in "${required[@]}"; do
  test -s "$dir/$name" || { echo "missing asset: $dir/$name"; exit 1; }
done

sha256sum -c "$dir/SHA256SUMS"

test "$("$dir/agentchat-linux-amd64" version)" = "agentchat $version"
for name in agentchat-windows-amd64.exe "AgentChatConsole-${version}.exe" "AgentChatConsole-${version}-installer.exe"; do
  test "$(head -c 2 "$dir/$name")" = "MZ" || { echo "not a PE: $name"; exit 1; }
done
test "$(stat -c%s "$dir/AgentChatConsole-${version}-installer.exe")" -gt 20000000 || {
  echo "installer is unexpectedly small"
  exit 1
}

if grep -a -q '192\.168\.[0-9]\+\.[0-9]\+' "$dir"/agentchat-* "$dir"/AgentChatConsole-*; then
  echo "private LAN address found in release artifacts"
  exit 1
fi

echo "release OK: $version"
