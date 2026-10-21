#!/usr/bin/env bash
# Build the loopback launcher plus a Linux desktop entry and a macOS .app bundle.
set -euo pipefail

cd "$(dirname "$0")/.."
version="${1:?usage: package-launcher.sh <version> [out-dir]}"
out="${2:-dist/launcher}"
numeric="${version#v}"
mkdir -p "$out"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

build() {
  local goos="$1" goarch="$2"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "-s -w" \
    -o "$out/agentchat-desktop_${version}_${goos}_${goarch}" ./cmd/agentchat-desktop
}

for arch in amd64 arm64; do
  build linux "$arch"
  build darwin "$arch"

  mkdir -p "$tmp/linux-$arch"
  cp "$out/agentchat-desktop_${version}_linux_${arch}" "$tmp/linux-$arch/agentchat-desktop"
  cp packaging/linux/agentchat.desktop "$tmp/linux-$arch/"
  tar -C "$tmp/linux-$arch" -czf "$out/agentchat-desktop_${version}_linux_${arch}.tar.gz" .

  mkdir -p "$tmp/darwin-$arch/AgentChat.app/Contents/MacOS"
  cp "$out/agentchat-desktop_${version}_darwin_${arch}" "$tmp/darwin-$arch/AgentChat.app/Contents/MacOS/agentchat-desktop"
  sed "s/__VERSION__/${numeric}/g" packaging/macos/Info.plist \
    > "$tmp/darwin-$arch/AgentChat.app/Contents/Info.plist"
  tar -C "$tmp/darwin-$arch" -czf "$out/agentchat-desktop_${version}_darwin_${arch}.app.tar.gz" AgentChat.app
done

ls -lh "$out"
