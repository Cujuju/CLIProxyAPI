#!/usr/bin/env bash
# Fork routine: merge router-for-me upstream/main into a sync/upstream-<YYYYMMDD> branch off origin/main,
# run the fork checks, push the branch to origin and open a PR. Merge that PR with "Create a merge commit":
# a squash or rebase drops upstream's history and makes the next sync conflict. Never pushes to upstream.
#
#   scripts/sync-upstream.sh [--dry-run]
#
# Runs from any checkout or worktree of the fork and leaves it untouched: the merge happens in a new
# worktree. On conflicts the script stops and prints that worktree's path; resolve and commit there, then
# rerun the script from it to run the checks, push and open the PR. --dry-run merges and checks, then
# removes the worktree and branch without pushing.
set -euo pipefail

dry_run=false
[ "${1:-}" = --dry-run ] && dry_run=true

start=$(git rev-parse --show-toplevel)
cd "$start"
origin_url=$(git remote get-url --push origin)
case "$origin_url" in
  *router-for-me/*) echo "origin points at upstream ($origin_url); this script only pushes to the fork." >&2; exit 1 ;;
esac
repo=$(printf '%s\n' "$origin_url" | sed -E 's#^.*github\.com[:/]##; s#\.git$##')

current=$(git branch --show-current)
if [[ "$current" == sync/upstream-* ]]; then
  # Rerun after resolving conflicts in the sync worktree.
  [ -z "$(git status --porcelain)" ] || { echo "Commit the merge first." >&2; exit 1; }
  branch=$current
  worktree=$PWD
  created=false
else
  git fetch upstream
  git fetch origin
  if git merge-base --is-ancestor upstream/main origin/main; then
    echo "origin/main already contains upstream/main."
    exit 0
  fi
  branch="sync/upstream-$(date +%Y%m%d)"
  if git show-ref --quiet "refs/heads/$branch" || git ls-remote --exit-code --heads origin "$branch" >/dev/null; then
    echo "$branch already exists locally or on origin; finish or delete it first." >&2
    exit 1
  fi
  worktree="$(mktemp -d)/CLIProxyAPI-sync"
  git worktree add -b "$branch" --no-track "$worktree" origin/main
  created=true
  cd "$worktree"
  if ! git merge --no-ff -m "chore: merge upstream main" upstream/main; then
    echo "Merge conflicts in $worktree. Resolve them, commit, then rerun scripts/sync-upstream.sh from there." >&2
    exit 1
  fi
fi

go build ./...
# The desktop app ships these targets; see CLAUDE.md "Fork checks".
out=$(mktemp -d)
for target in darwin/arm64 darwin/amd64 linux/amd64 windows/amd64; do
  CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go build -o "$out/server-${target/\//-}" ./cmd/server
done
rm -rf "$out"
go test ./...

count=$(git rev-list --count origin/main..HEAD^2 2>/dev/null || echo '?')
title="chore: merge upstream main ($(date +%Y-%m-%d))"
body="Merges ${count} commits from router-for-me/CLIProxyAPI \`main\` (via \`scripts/sync-upstream.sh\`). Build, desktop cross-compiles and \`go test ./...\` passed locally.

**Merge with \"Create a merge commit\". Never squash or rebase**: the fork must keep upstream's commits so the next sync merges cleanly."

if $dry_run; then
  echo "Dry run: merged ${count} upstream commits on $branch; checks passed. Would run:"
  echo "  git push -u origin $branch"
  echo "  gh pr create --repo $repo --base main --head $branch --title \"$title\""
  if $created; then
    cd "$start"
    git worktree remove --force "$worktree"
    git branch -D "$branch" >/dev/null
  fi
  exit 0
fi

# Push explicitly to origin (the fork), never to upstream.
git push -u origin "$branch"
gh pr create --repo "$repo" --base main --head "$branch" --title "$title" --body "$body"
if $created; then
  cd "$start"
  git worktree remove "$worktree"
  echo "Removed $worktree; $branch stays on origin."
else
  echo "Pushed $branch. Remove this worktree when done: git worktree remove \"$worktree\""
fi
