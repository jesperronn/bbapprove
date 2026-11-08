# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow semver.

## [Unreleased]

### Fixed
- The ignore list now wins over every other skip reason. A PR matching an ignore entry is `IGNORED` even when
  it is a major bump, disallowed, already approved or not a Renovate PR (before, the ignore check came last,
  so such a PR stayed `SKIP` and pressing `i` on it wrote the entry while the row visibly did nothing). Ignored
  PRs are still never acted on; un-ignoring restores the original `SKIP` reason.
- The non-interactive CLI (dry run and `--apply`) now honours the per-user ignore list. A PR matching an
  entry is `IGNORED` and is never approved, merged, declined or commented. An unreadable ignore file stops
  the run instead of being treated as empty. The CLI and TUI share one classification (`rules.Classify`).

### Added
- CLI shows ignored PRs dim with `IGNORED` and the reason `on your ignore list: <entry>`; `--hide-ignored`
  omits them and the footer says `N ignored (hidden)`.
- TUI: `h` is an alias of `.` (show/hide ignored rows).
- TUI exit summary: on `q` / ctrl+c the actions of the session (merged, not merged, declined, commented,
  failed, ignored, un-ignored) are printed as a table plus a tally, or `No changes made.`; rows still running
  are listed as `WORKING…`. Truncated to the width on a TTY only, honours `NO_COLOR`.

### Changed
- STATUS column redesigned as the row's action state and widened from 8 to 11 characters (CLI and TUI):
  eligible and untouched rows are blank; words are `SKIP`, `IGNORED`, `APPROVING…`, `DECLINING…`,
  `COMMENTING…`, `MERGED`, `DECLINED`, `COMMENTED`, `NOT MERGED` (was `APPROVED, NOT MERGED`) and `FAIL`.
  `ELIGIBLE` no longer exists. A comment result now shows on the row (and a later action replaces it).
- TUI keys: lowercase `a` (approve+merge, instant, no prompt), `d`, `c`, `i`, `o` act on the cursor row
  only and never read or change marks. Uppercase `A`, `D`, `C` act on the marked rows (confirm counts,
  one shared comment; acted rows are un-marked; "no marked rows" with none).
- `ctrl+a` replaces `X` for marking all eligible rows.
- Footer redesigned as two never-wrapping lines: message / prompt / cursor row URL with the dim version
  right-aligned, then compact key hints (as many as fit) after `N PRs, M marked`. `?` opens extended help.

### Added
- Cursor row gets a full-width background (kept across inner colour resets; off with `NO_COLOR`), a bold
  yellow mark bar, and a fainter hover background.
- `r` reloads the list, keeping cursor and marks by PR URL.
- Mouse support: click to move the cursor, click the mark column to toggle, wheel scroll, hover highlight.
  Disable with `--no-mouse` or `BBAPPROVE_MOUSE=0` (capture disables native text selection; Shift bypasses it).

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
