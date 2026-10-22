#!/usr/bin/env bash
# Regenerate the Windows icon resources embedded in the console/installer.
# Requires github.com/akavel/rsrc on PATH.
set -euo pipefail
cd "$(dirname "$0")/.."
test -f assets/agentchat.ico
rsrc -ico assets/agentchat.ico -o cmd/agentchat-desktop/rsrc_windows_amd64.syso
rsrc -ico assets/agentchat.ico -o cmd/agentchat-installer/rsrc_windows_amd64.syso
ls -lh cmd/agentchat-desktop/rsrc_windows_amd64.syso cmd/agentchat-installer/rsrc_windows_amd64.syso
