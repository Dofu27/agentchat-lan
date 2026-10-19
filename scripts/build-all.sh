#!/usr/bin/env bash
# Build release binaries for the supported platforms (no CGO).
set -euo pipefail

cd "$(dirname "$0")/.."
mkdir -p dist

version="${VERSION:-dev}"

for target in linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  ext=""
  if [[ "$os" == "windows" ]]; then
    ext=".exe"
  fi
  echo "building ${os}/${arch}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=${version}" \
    -o "dist/agentchat-${os}-${arch}${ext}" ./cmd/agentchat
done

(
  cd dist
  sha256sum agentchat-* >SHA256SUMS
)

ls -lh dist
