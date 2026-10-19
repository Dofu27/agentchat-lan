#!/usr/bin/env sh
# Configure per-agent Git identity, commit trailer, and local hook.
set -eu

agent="${1:-}"
case "$agent" in
    dsh)
        name="DSH"
        email="dsh@localhost"
        ;;
    codex)
        name="Codex"
        email="codex@localhost"
        ;;
    *)
        echo "usage: $0 dsh|codex" >&2
        exit 2
        ;;
esac

git config user.name "$name"
git config user.email "$email"
git config agent.identity "$agent"
git config core.hooksPath .githooks
chmod +x .githooks/commit-msg 2>/dev/null || true

echo "identity: $name <$email> (Agent: $agent)"
echo "hooks: .githooks/commit-msg"
