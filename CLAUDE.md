# CLIProxyAPI (Cujuju private fork)

See AGENTS.md for upstream conventions. Desktop-facing API: docs/desktop-api.md.

## Tracker
- GitHub Issues: `Cujuju/CLIProxyAPI`
- Labels: `area/routing`, `area/server`, `area/desktop`, `kind/feature`; arc `arc/account-rotation`
- `upstream` remote push is disabled; never push to router-for-me.

## Fork checks
- The desktop app ships Windows and macOS builds. Cross-compile before committing: `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./cmd/server` (and `GOOS=linux`).
- Go treats a `_<GOOS>.go` / `_<GOARCH>.go` file-name suffix as a build constraint (e.g. `quota_windows.go` builds only on Windows). Name files so they don't end in one unless that's intended.
