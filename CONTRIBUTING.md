<!-- Fork section (Cujuju/CLIProxyAPI). Keep it at the top; upstream text, if any, goes below it unchanged. -->
## About this fork

This is a fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It backs the [Spillway](https://github.com/Cujuju/spillway) desktop app: it adds a desktop supervisor API ([docs/desktop-api.md](docs/desktop-api.md)), per-account quota and usage reporting, and quota-aware account routing.

- Changes that aren't specific to this fork belong upstream. Open them at [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).
- PRs here target `main`. The maintainer picks the merge method.
- Upstream syncs come in as PRs from `sync/upstream-<YYYYMMDD>` branches (`scripts/sync-upstream.sh`). They must be merged with a merge commit, never squashed or rebased, so later syncs stay clean.
