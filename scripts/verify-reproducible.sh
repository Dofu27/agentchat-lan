#!/usr/bin/env bash
# Build the Linux CLI twice from the same checkout and require identical bytes.
set -euo pipefail

cd "$(dirname "$0")/.."
version="${1:?usage: verify-reproducible.sh <version>}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

for n in 1 2; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=${version}" \
    -o "$tmp/agentchat-$n" ./cmd/agentchat
done

cmp "$tmp/agentchat-1" "$tmp/agentchat-2"
sha256sum "$tmp/agentchat-1" "$tmp/agentchat-2"
echo "reproducible OK: $version"
