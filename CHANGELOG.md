# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow semver.

## [Unreleased]

### Added
- `bbapprove doctor` prints one line per area; every problem carries its fix on its own line and the last line only counts them. `bbapprove doctor -v` adds the lookup order, file mode, tool paths and environment, without printing any fact twice.
- Maintainers: `bin/test --timings` prints the slowest shell suites, Go packages and Go tests and the total run time.
- Maintainers: `bin/lint` checks shell formatting with shfmt when it is installed, and `bin/lint --autofix` rewrites it.
- Maintainers: `bin/demo` runs bbapprove on fictional PRs with no server, with `--help` per subcommand. `bin/demo regenerate` records the demo animation from a tmux session, draws it with `freeze` and a small Go program in `demo/cast2svg`, and writes the SVG screenshots with `freeze`; it needs only tmux, freeze and Go, and neither needs Homebrew.

### Fixed
- The `?` help in the TUI wraps to the terminal width instead of cutting lines off, and leaves the list at least four rows on a short terminal.

### Internal
- A fake Bitbucket Server (`internal/bbfake`) and golden characterization tests that pin the bash Bitbucket adapter's exact behaviour, as a safety net for porting it to Go.
- Go tests share one fixed fake adapter and fake editor script per test binary instead of writing a script per test, and the parallel-apply test sleeps less; the Go suite runs about twice as fast.
- Shell test suites use fixed, env-driven stubs under `test/fixtures` (git, gh, curl, go, recorders) instead of
  writing scripts per test.

## [1.0.0] - 2026-11-09

### Changed (BREAKING)
- The token comes from `BBAPPROVE_TOKEN` (wins when set), or else from the 1Password item named by
  `BBAPPROVE_TOKEN_NAME`, `--token-name NAME` or `user_token_name` (precedence flag > env > file; looked up with
  `op`, field `token`, then `credential`). The item name has no built-in default any more (it was `BITBUCKET_TOKEN`).
  With neither a token nor an item name, runs that need the forge exit 1 before calling the adapter and
  `bbapprove doctor` reports it as a problem. `--token` was removed (a token on the command line is visible to
  other users); the adapter's `auth token` remains and runs only when `BBAPPROVE_TOKEN` is empty and a name is set.

### Changed
- The README is a short task-led quick-start (install, set up, first run, TUI keys, update, troubleshoot) that links
  to `bbapprove --help` and its topics instead of repeating flag and variable tables. `bbapprove help` without a
  topic now lists the topics instead of repeating `--help`.

- `--outdated`, `--update`, `--init`, the first-run prompts and every error message (unknown option with its usage,
  unknown help topic, missing config, missing token) are coloured like `--help` and `doctor`: version numbers, paths,
  flags and the printed `ln -sfn` switch and roll-back commands are highlighted, "up to date" is green, "update
  available" yellow, errors red. Colour is decided per stream (a terminal on stdout does not colour stderr); `NO_COLOR`
  wins, `FORCE_COLOR=1` forces. Without colour the text is byte-for-byte what it was; `--version` stays plain.

### Added
- `bbapprove help TOPIC` for `rules`, `tui`, `config`, `token`, `update` and `env` (next to `doctor`); `bbapprove help`
  lists them. The top-level `--help` now fits one screen and points at the topics.
- Coloured `--help`, `bbapprove doctor` and `--print-config` (terminal only; `NO_COLOR` disables, `FORCE_COLOR=1`
  forces; piped output is unchanged plain text). New `bbapprove doctor --help` (also `bbapprove help doctor`).
- `bbapprove doctor [--offline] [--config FILE]`: read-only diagnostics (version and update status, every config
  file candidate and which one is used, setting sources, adapter, which token source is used (never a value; `op` is not run), ignore and
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

- TUI footer: the cursor row's PR URL is a clickable OSC 8 hyperlink that opens on click, an `o open` hint always
  follows it, the URL is shortened in the middle when the window is narrow, `o open` is added to the Shift-held hint
  set and is the last hint dropped on narrow windows.

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
