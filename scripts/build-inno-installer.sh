#!/usr/bin/env bash
set -euo pipefail

# Build the Inno Setup wizard. Requires ISCC.exe (Inno Setup 6) on PATH or
# ISCC environment variable pointing to it.
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${VERSION:-0.30.25}"
STAGE="$ROOT/build/inno-stage"
DIST="$ROOT/dist"
mkdir -p "$STAGE" "$DIST"

GO_BIN="${GO_BIN:-go}"
WINRES="${WINRES:-go-winres}"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0

"$WINRES" make --in "$ROOT/winres/desktop.json" --out "$ROOT/cmd/agentchat-desktop/rsrc" --arch amd64
"$WINRES" make --in "$ROOT/winres/cli.json" --out "$ROOT/cmd/agentchat/rsrc" --arch amd64
"$GO_BIN" build -trimpath -ldflags="-H=windowsgui -s -w" -o "$STAGE/AgentChatConsole.exe" "$ROOT/cmd/agentchat-desktop"
"$GO_BIN" build -trimpath -ldflags="-s -w" -o "$STAGE/agentchat.exe" "$ROOT/cmd/agentchat"

WEBVIEW2="$STAGE/MicrosoftEdgeWebView2RuntimeInstallerX64.exe"
WEBVIEW2_SHA="04b9f08d839c8c06f34a85acea0d9f1568d3d8aa309a77619aaa46bb29ade0f8"
if [ ! -f "$WEBVIEW2" ]; then
  curl -L --fail --retry 3 -o "$WEBVIEW2" \
    "https://msedge.sf.dl.delivery.mp.microsoft.com/filestreamingservice/files/f3274495-ff02-440e-b522-2d4129a911e8/MicrosoftEdgeWebView2RuntimeInstallerX64.exe"
fi
printf '%s  %s\n' "$WEBVIEW2_SHA" "$WEBVIEW2" | sha256sum -c -

ISCC_BIN="${ISCC:-ISCC.exe}"
"$ISCC_BIN" "/DAppVersion=$VERSION" "/DStageDir=$STAGE" "$ROOT/installer/agentchat.iss"
