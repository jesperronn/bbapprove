# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow semver.

## [1.0.0-rc.1]

### Added
- Go rewrite of the former bash core (`cmd/bbapprove`, `internal/`), with dry run by default and `--apply`.
- Forge adapter interface (a `gh`-style CLI run as a subprocess) with a Bitbucket Server adapter.
- Interactive TUI (`-i`): mark rows, approve and merge, decline (with optional comment), comment,
  ignore a dependency, open in browser.
- Check columns: build status, SonarQube quality gate, approvals, comment count, open tasks.
- Parallel requests with timeouts when listing and merging PRs (`BBAPPROVE_JOBS`).
- `disallow.txt` as a repo-wide hard block, plus a per-user ignore list.
- `bin/bbapprove` launcher that rebuilds the Go binary when the sources are newer.
- `bin/build` with versioning from `git describe` (`--version`, `-V`, shown in help and TUI footer),
  `--test` pre-checks and `--install` to symlink the launcher into `~/.local/bin`.
