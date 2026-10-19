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
go_files="$(grep -rl --exclude-dir=.git --exclude-dir=bin --exclude-dir=dist \
  --exclude-dir=vendor --include='*.go' "$old" . || true)"
if [[ -n "$go_files" ]]; then
  printf '%s\n' "$go_files" | xargs -r sed -i "s|$old|$new|g"
fi

go mod edit -module "$new"
go mod tidy

# README clone URLs and placeholder references.
md_files="$(grep -rl --exclude-dir=.git --exclude-dir=bin --exclude-dir=dist \
  --exclude-dir=vendor --include='*.md' "$old" . || true)"
if [[ -n "$md_files" ]]; then
  printf '%s\n' "$md_files" | xargs -r sed -i "s|$old|$new|g"
fi

echo "done. run: gofmt -w . && go test ./..."
