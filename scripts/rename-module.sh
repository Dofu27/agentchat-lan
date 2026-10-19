#!/usr/bin/env bash
# Rename the Go module and update README clone URLs before publishing.
set -euo pipefail

new="${1:-}"
if [[ -z "$new" ]]; then
  echo "usage: $0 github.com/<owner>/<repo>" >&2
  exit 2
fi
case "$new" in
  github.com/*/*) ;;
  *) echo "module path must start with github.com/<owner>/<repo>" >&2; exit 2 ;;
esac

old="$(go list -m)"
if [[ "$old" == "$new" ]]; then
  echo "module is already $new"
  exit 0
fi

echo "renaming $old -> $new"
# Update Go imports. Skip .git, bin, dist, and vendor.
grep -rl --exclude-dir=.git --exclude-dir=bin --exclude-dir=dist \
  --exclude-dir=vendor --include='*.go' "$old" . \
  | xargs -r sed -i "s|$old|$new|g"

go mod edit -module "$new"
go mod tidy

# README clone URLs and placeholder references.
grep -rl --exclude-dir=.git --exclude-dir=bin --exclude-dir=dist \
  --exclude-dir=vendor --include='*.md' "$old" . \
  | xargs -r sed -i "s|$old|$new|g"

echo "done. run: gofmt -w . && go test ./..."
