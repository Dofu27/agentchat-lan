#!/usr/bin/env bash
# Read-only preflight for a production node. Usage:
#   scripts/check-node.sh <host> [user] [port]
set -euo pipefail

host="${1:?usage: check-node.sh <host> [user] [port]}"
user="${2:-tofu}"
port="${3:-22}"

ssh -o BatchMode=yes -o ConnectTimeout=8 -p "$port" "$user@$host" '
  set -e
  echo "host=$(hostname)"
  echo "user=$(id -un) uid=$(id -u)"
  echo "os=$(uname -sr)"
  if command -v docker >/dev/null 2>&1; then
    echo "runtime=docker"
    docker ps --format "container={{.Names}} image={{.Image}} ports={{.Ports}}"
  elif command -v systemctl >/dev/null 2>&1; then
    echo "runtime=systemd"
    systemctl is-active agentchat 2>/dev/null || true
  else
    echo "runtime=unknown"
  fi
  for dir in "$HOME/agentchat-data" "$HOME/agentchat-tls"; do
    if [ -d "$dir" ]; then
      echo "dir=$dir owner=$(stat -c "%U:%G %a" "$dir")"
    else
      echo "dir=$dir missing"
    fi
  done
'
