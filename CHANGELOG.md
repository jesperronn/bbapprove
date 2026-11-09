# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow semver.

## [Unreleased]

### Added
- `bbapprove doctor [--offline] [--config FILE]`: read-only diagnostics (version and update status, every config
  file candidate and which one is used, setting sources, adapter, token sources without the token, ignore and
  disallow lists, `BBAPPROVE_*` variables, `git`/`gh`/`op`). Exit 1 when a problem is found. A non-URL argument
  now fails early with `Not a PR URL: <x>` and a hint at the subcommands (exit 2).
- The CLI footer names the ignore file: `N ignored via <path> (see: bbapprove doctor)`; `--hide-ignored` adds the
  path to its `N ignored (hidden)` line.
- First-run config creation: without a config file, `bbapprove` on a terminal offers to create
  `~/.config/bbapprove/config.toml` (mode 0600, never overwrites) from the template built into the binary, opens it
  in `$VISUAL`/`$EDITOR` and re-prompts while the example values are unchanged. In CI or pipes nothing is written.
- `bbapprove --init [--force] [--config FILE]` creates the config on request.
- `bin/setup`, `bin/install`, `bin/help` and `bin/release` (changelog, tag, push, watch the release workflow).
- Maintainer docs under `docs/` (development, architecture, releasing, CI) and `AGENTS.md`.
- `bbapprove --outdated [--pre]` checks read-only whether a newer release exists (exit 0 up to date, 1 outdated,
  2 could not check). It asks `gh release list`, or `git ls-remote --tags origin` in a checkout without `gh`.
- `bbapprove --update [--yes] [--pre]` shows a plan and asks `Proceed? [y/N]` (`--yes` skips it). A git checkout is
  updated in place (fast-forward on `main`, then `bin/build`). Any other install gets the new release unpacked
  side by side into a new sibling directory after its SHA-256 check, and prints the switch and rollback commands;
  nothing is switched automatically.
- Config key `update_repo` and env `BBAPPROVE_UPDATE_REPO` choose the release repository for both (default
  `jesperronn/bbapprove`).

### Changed
- `bin/runtests` was renamed to `bin/test`.

## [1.0.0-rc.2] - 2026-11-09

### Fixed
- The ignore list now wins over every other skip reason. A PR matching an ignore entry is `IGNORED` even when
  it is a major bump, disallowed, already approved or not a Renovate PR (before, the ignore check came last,
  so such a PR stayed `SKIP` and pressing `i` on it wrote the entry while the row visibly did nothing). Ignored
  PRs are still never acted on; un-ignoring restores the original `SKIP` reason.
- The non-interactive CLI (dry run and `--apply`) now honours the per-user ignore list. A PR matching an
  entry is `IGNORED` and is never approved, merged, declined or commented. An unreadable ignore file stops
  the run instead of being treated as empty. The CLI and TUI share one classification (`rules.Classify`).

### Added
- TUI: the keyboard hint line follows the Shift key. Holding Shift shows the batch keys (`A approve marked  D decline
  marked  C comment marked  ctrl+a mark all  x mark  ? help  q quit`), releasing it restores the instant keys.
  Needs the kitty keyboard protocol (Ghostty, kitty, recent iTerm2, WezTerm); other terminals keep the instant
  set. `BBAPPROVE_KITTY=0` turns the request off. The `h ignored` hint was dropped from the line (still in `?`).
- CLI shows ignored PRs dim with `IGNORED` and the reason `on your ignore list: <entry>`; `--hide-ignored`
  omits them and the footer says `N ignored (hidden)`.
- TUI: `h` is an alias of `.` (show/hide ignored rows).
- TUI exit summary: on `q` / ctrl+c the actions of the session (merged, not merged, declined, commented,
  failed, ignored, un-ignored) are printed as a table plus a tally, or `No changes made.`; rows still running
  are listed as `WORKING…`. Truncated to the width on a TTY only, honours `NO_COLOR`.
- Cursor row gets a full-width background (kept across inner colour resets; off with `NO_COLOR`), a bold
  yellow mark bar, and a fainter hover background.
- `r` reloads the list, keeping cursor and marks by PR URL.
- Mouse support: click to move the cursor, click the mark column to toggle, wheel scroll, hover highlight.
  Disable with `--no-mouse` or `BBAPPROVE_MOUSE=0` (capture disables native text selection; Shift bypasses it).
- MIT license (`LICENSE`).
- TOML config file (`bbapprove.config.toml`, git-ignored; template `bbapprove.config.template`) with `type`,
  `repository`, `user_token_name`, `author_email` and `disallow`. Looked up via `--config` / `BBAPPROVE_CONFIG`, the
  repo root, then `$XDG_CONFIG_HOME/bbapprove/config.toml`. Precedence: command line > environment > file > default.
  Unknown keys warn, malformed TOML names file and line.
- `--config FILE`, `--token-name NAME`, `--print-config` (effective settings and their sources, no secrets) and the
  env vars `BBAPPROVE_CONFIG`, `BBAPPROVE_REPOSITORY`, `BBAPPROVE_TOKEN_NAME`.
- GitHub Actions CI (`bin/lint` and `bin/runtests` on Linux and macOS), a tag-driven release workflow
  (cross-built `.tar.gz` archives for darwin and linux on arm64/amd64, `SHA256SUMS`, notes from this changelog,
  pre-release for `-rc` tags) and Dependabot for `gomod` and `github-actions` with `chore(deps):` titles.
- `bin/release-notes <tag>` prints the matching CHANGELOG.md section.

### Changed
- Internal: the TUI now uses Bubble Tea v2 (`charm.land/bubbletea/v2`); no behaviour change.
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
- **BREAKING:** the built-in site defaults are gone. `repository` (the forge URL) and `author_email` must now be
  configured, and the tracked `disallow.txt` was removed. Migrate: `cp bbapprove.config.template bbapprove.config.toml`,
  fill in `repository`, `user_token_name`, `author_email` and `disallow`, then check with `bbapprove --print-config`.
- The bash adapter no longer has a default `BBAPPROVE_BASE_URL` (it exits 2 when unset) and takes the
  1Password item from `BBAPPROVE_TOKEN_NAME` (default `BITBUCKET_TOKEN`).
- Release archives ship `bbapprove.config.template` instead of `disallow.txt`.

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
