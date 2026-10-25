#!/usr/bin/env bash
# Fork routine: merge router-for-me upstream/main into this fork's main, then run the fork checks.
# Merges (never rebases) because main is published. Never pushes; review, then `git push origin main`.
# Run from a clean checkout of main: scripts/sync-upstream.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

[ "$(git branch --show-current)" = main ] || { echo "Check out main first." >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "Working tree is not clean." >&2; exit 1; }

git fetch upstream
git fetch origin
git merge --ff-only origin/main
before=$(git rev-parse HEAD)
if ! git merge --no-ff -m "chore: merge upstream main" upstream/main; then
  echo "Merge conflicts. Resolve them, run the checks below, then commit." >&2
  exit 1
fi
if [ "$(git rev-parse HEAD)" = "$before" ]; then
  echo "Already up to date with upstream/main."
  exit 0
fi

go build ./...
# The desktop app ships these targets; see CLAUDE.md "Fork checks".
out=$(mktemp -d)
for target in darwin/arm64 darwin/amd64 linux/amd64 windows/amd64; do
  CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go build -o "$out/server-${target/\//-}" ./cmd/server
done
rm -rf "$out"
go test ./...

echo "Merged $(git rev-list --count "$before"..HEAD^2 2>/dev/null || echo '?') upstream commits. Review, then: git push origin main"
