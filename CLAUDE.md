# CLIProxyAPI (Cujuju private fork)

See AGENTS.md for upstream conventions. Desktop-facing API: docs/desktop-api.md.

## Tracker
- GitHub Issues: `Cujuju/CLIProxyAPI`
- Labels: `area/routing`, `area/server`, `area/desktop`, `kind/feature`; arc `arc/account-rotation`
- `upstream` remote push is disabled; never push to router-for-me.

## Fork checks
- The desktop app ships Windows and macOS builds. Cross-compile before committing: `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/server` (and `GOOS=linux`).
- Go treats a `_<GOOS>.go` / `_<GOARCH>.go` file-name suffix as a build constraint (e.g. `quota_windows.go` builds only on Windows). Name files so they don't end in one unless that's intended.

## Upstream sync
- `scripts/sync-upstream.sh` on a clean `main`: merges `upstream/main` (merge, not rebase: `main` is published), builds, cross-compiles the desktop targets and runs `go test ./...`. It never pushes.
- Known upstream failures on Windows (they fail at the pre-fork commit too, so not merge regressions): `TestResponsesInterruptStopsHTTPUpstream` (sdk/api/handlers/openai), `TestToolSearchDisabledMatchesDevGolden` (internal/translator/claude/openai/responses), `TestAdvertiserAndBrowser_Integration` (internal/discovery, needs mDNS).
- Fork-only changes worth re-checking after a merge: `sdk/cliproxy/auth` (soonest-reset, quota exhaustion, first-byte timeout, selection IDs, preferred auth, usage-lookup snapshot `QuotaState.LookupSignals`) and `internal/api/handlers/management/desktop_*.go`.
