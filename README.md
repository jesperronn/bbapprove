# bbapprove

Approve and merge Renovate PRs on Bitbucket Server via the REST API.
Dry-run by default; `--apply` approves, then merges.

The core is a Go program (`cmd/bbapprove`, `internal/`). `bin/bbapprove` is a small bash launcher:
it (through `bin/build`) builds `bin/bbapprove-go` when it is missing or older than the Go sources (`go.mod`, `go.sum`,
`cmd/**/*.go`, `internal/**/*.go`) and then execs it with all arguments unchanged. Building needs Go
on PATH (https://go.dev/dl/); without it and without a built binary the launcher exits 127.

## Usage

```
bin/bbapprove                      # dry run: list PRs where I'm reviewer; blank status = eligible, SKIP/IGNORED carry a reason
bin/bbapprove --apply              # approve + merge the eligible ones
bin/bbapprove --apply <PR_URL>...  # only the given PRs (PR URLs skip the reviewer list)
bin/bbapprove -i                   # interactive TUI (see below)
bin/bbapprove --help               # flags, eligibility rules, env vars
```

Flags: `--apply`, `--hide-ignored`, `--disallow a,b` (adds to the disallow list), `--token-name NAME`,
`--config FILE`, `--print-config`, `--init [--force]`, `--outdated [--pre]`, `--update [--yes] [--pre]`,
`-i/--interactive`, `-h/--help`.

Eligible, all of:
- author is the configured `author_email` (no default, see Configuration)
- subject starts `chore(deps):` or `fix(deps):`
- not on the disallow list (`disallow` in the config file, merged with `--disallow` / `BBAPPROVE_DISALLOW`). Case-insensitive substring match on the title.
- minor/patch/digest only. Current heuristic: a title ending `to vN` or a branch `renovate/major-*` counts as major. This is not reliable yet, see TODO.md.
- not already approved by you
- not on your per-user ignore list (see below)

Anything else is reported as SKIP with the reason. Nothing is changed without `--apply`. The `STATUS`
cell of an eligible, untouched PR is blank.

### Ignore list in the CLI

The per-user ignore list (`~/.config/bbapprove/ignore`, or `$XDG_CONFIG_HOME/bbapprove/ignore`) is honoured by
the plain CLI and `--apply`, not only by the TUI. A PR whose title matches an entry (case-insensitive substring)
is classified `IGNORED`: it is shown dim with the reason `on your ignore list: <entry>` after the title and is
never approved, merged, declined or commented, in dry run and with `--apply`. A missing file is an empty list;
a file that exists but cannot be read stops the run (exit 1) rather than risk acting on an ignored PR.
`--hide-ignored` leaves the ignored rows out of the table; the footer then says `N ignored (hidden) via <ignore file>`. Without it a line `N ignored via <ignore file> (see: bbapprove doctor)` precedes the dry-run line.
The ignore list wins over every other skip reason: a PR matching an entry is `IGNORED` even if it would
otherwise be `SKIP` (author, subject, disallow, major, already approved), and an ignored PR is never acted
on. Removing the entry restores the PR's own `SKIP` reason. The CLI and the TUI share one classification
function (`rules.Classify`).

## First run

With no config file, `bbapprove` on a terminal offers to create `$XDG_CONFIG_HOME/bbapprove/config.toml` (default
`~/.config/bbapprove/config.toml`, mode 0600, never overwrites) from the template built into the binary, opens it in
`$VISUAL`/`$EDITOR`, re-prompts while the example values are unchanged (at most 3 rounds), then continues with your
original command once the config is valid. In CI or pipes nothing is written; the error shows the exact
`mkdir -p ... && cp bbapprove.config.template ...` command to run.

```
bbapprove --init [--force] [--config FILE]   # the same on request; --force replaces an existing file
bbapprove --print-config                     # effective settings; says how to create a config when none exists
```

`--init` writes to `--config FILE`, else `$BBAPPROVE_CONFIG`, else the default path above.

## Troubleshooting

```
bbapprove doctor [--offline] [--config FILE]
bbapprove doctor --help
```

Read-only diagnostics, no token and no forge access needed: version and whether a newer release exists, the
config file in use and every candidate path with `used` / `shadowed` / `not found`, where each setting came from,
the adapter, the token source (a FAIL when there is none; never a value, `op` is only looked for on `PATH`, never run), the
ignore and disallow lists, `BBAPPROVE_*` variables, and `git` / `gh` / `op`. Exit 0 when nothing FAILed, 1 otherwise;
`--offline` skips the update check; `bbapprove doctor --help` explains each section. Typical output:

```
config:
        1. --config flag     (not set)
        3. repo root         /opt/bbapprove/bbapprove.config.toml  [not found]
        4. XDG config        /home/me/.config/bbapprove/config.toml  [found, used]
ok   in use:       /home/me/.config/bbapprove/config.toml (via XDG config)
ignore list:
     path:         /home/me/.config/bbapprove/ignore
     entries:      1
       @some/library
all good
```

Why is a row `IGNORED — on your ignore list: @some/library`? Because that entry is in the ignore file shown under
`ignore list:` (`$XDG_CONFIG_HOME/bbapprove/ignore`, default `~/.config/bbapprove/ignore`; one dependency per line).
Edit the file, or press `i` on an ignored row in the TUI, to remove the entry. The CLI footer also names the file.

### Colours

`--help`, `bbapprove doctor` (and `doctor --help`) and `--print-config` are coloured with the standard 16 ANSI colours,
so they follow your terminal theme: headings bold cyan, subcommands bold magenta, flags green, placeholders bright blue,
environment variables yellow, paths cyan, notes dim; `ok` / `warn` / `FAIL` green / yellow / bold red and the setting
sources `[flag]` `[env]` `[file]` `[default]` magenta / yellow / green / dim. Colour is used only on a terminal;
`NO_COLOR` turns it off (and wins over `FORCE_COLOR`), `FORCE_COLOR=1` forces it on, e.g. into a pager.
Piped output is plain text.

## Updating

bbapprove never checks for updates by itself. You ask for it:

```
bbapprove --outdated [--pre]          # read-only check: exit 0 up to date, 1 outdated, 2 could not check
bbapprove --update [--yes] [--pre]    # shows a plan, then asks Proceed? [y/N]; --yes skips the question
```

Neither command needs a config file, a token or forge access.

- **Check.** `--outdated` asks `gh release list` for the newest release. In a git checkout without `gh` it falls back
  to `git ls-remote --tags origin`. A pre-release build compares against the newest release including pre-releases;
  a stable build compares against the newest stable release unless you pass `--pre`. The repository defaults to
  `jesperronn/bbapprove`; override it with `BBAPPROVE_UPDATE_REPO` or `update_repo` in the config file.
- **Confirm.** `--update` runs the same check and prints a plan. The default answer is no. Without a terminal it
  refuses to change anything unless you pass `--yes`.
- **Git checkout.** Runs `git fetch --tags`, `git pull --ff-only` and `bin/build`. It needs a clean tree on `main`
  that can be fast-forwarded; otherwise nothing is changed.
- **Release archive.** The new release is unpacked into a new directory next to the current install, named
  `bbapprove-<tag>-<os>-<arch>`, after its SHA-256 has been verified against the release's `SHA256SUMS` (needs `gh`).
  The running tree is never modified and nothing is switched automatically. The command prints how to switch to the
  new tree and how to roll back (`ln -sfn` lines when your `bbapprove` is a symlink). Your config in
  `$XDG_CONFIG_HOME/bbapprove/config.toml` is shared by both trees; see [Configuration](#configuration).
- `SHA256SUMS` comes from the same release as the archive, so the check protects against corruption and partial
  downloads, not against a compromised release. Build provenance and attestation checks are future work
  (see [TODO.md](TODO.md)).

## Configuration

Nothing site- or person-specific is built in. The easiest start is the first-run flow above; by hand, copy the
committed template and edit it (the repo-root `bbapprove.config.toml` is git-ignored):

```
cp bbapprove.config.template bbapprove.config.toml
```

```toml
type = "bitbucket"                              # forge adapter: adapters/<type>; default "bitbucket"
repository = "https://bitbucket.example.com"    # base URL of the forge; required, no default
# user_token_name = "my-1password-item"       # optional 1Password item holding the token; no default; BBAPPROVE_TOKEN wins
author_email = "renovate-bot@example.com"       # only PRs by this bot are eligible; required, no default
disallow = ["some-library", "another-library"]  # never auto-approve; merged with --disallow
update_repo = "jesperronn/bbapprove"            # GitHub owner/repo for --outdated and --update; this is the default
```

**File lookup** (the first that exists is used): `--config FILE`, `$BBAPPROVE_CONFIG` (both must exist, else an
error), `<repo root>/bbapprove.config.toml` (the parent of the binary's directory, like `adapters/`), then
`$XDG_CONFIG_HOME/bbapprove/config.toml` (default `~/.config/bbapprove/config.toml`).

**Precedence** per setting: command line > environment > config file > built-in default. The environment names are
`BBAPPROVE_REPOSITORY`, `BBAPPROVE_TOKEN_NAME`, `BBAPPROVE_AUTHOR_EMAIL` and `BBAPPROVE_UPDATE_REPO`; the command line has `--token-name NAME`.
Only `type` and `update_repo` have built-in defaults (`user_token_name` has none). `disallow` is the union of the file, `--disallow` /
`BBAPPROVE_DISALLOW` and the optional legacy `BBAPPROVE_DISALLOW_FILE` (one dependency per line; default `disallow.txt`
in the repo root, git-ignored). `BBAPPROVE_ADAPTER` (an executable path) overrides what `type` selects.

Unknown keys only print a warning (typo catching); malformed TOML stops the run and names the file and line.
The bash adapter never reads TOML: the Go core hands it the resolved values as `BBAPPROVE_BASE_URL` and
`BBAPPROVE_TOKEN_NAME`.

Without `repository` or `author_email` bbapprove exits 1, names the missing setting and lists the paths it searched.

**Debugging**: `bbapprove --print-config` prints the effective settings and whether each came from the flag, the
environment, the file or a default. It never prints a token and never contacts the forge.

### Token

The token is resolved in this order:

1. `BBAPPROVE_TOKEN`, when set and non-empty. It wins; 1Password is not consulted.
2. Otherwise the 1Password item named by `--token-name NAME`, `BBAPPROVE_TOKEN_NAME` or `user_token_name`
   (that precedence). bbapprove runs `op item get "<NAME>" --reveal --fields label=token` and, if that fails or is
   empty, retries with `--fields label=credential`. There is no default item name.
3. Otherwise every run that talks to the forge (the dry run, `--apply`, `-i`) stops at once with exit 1, before the
   adapter is called. `--help`, `--print-config`, `--init`, `--outdated`, `--update` and `doctor` do not need a
   token; `bbapprove doctor` flags a missing source as a problem and never looks the item up.

Create a Bitbucket Server / Data Center HTTP access token (personal access token) for the account that reviews
the PRs. The adapter reads the PR dashboard, build status and PR details, so the token needs repository read
access, and write access to approve, merge, comment on and decline PRs (`--apply`, TUI actions).

```
export BBAPPROVE_TOKEN=...                      # shell profile or direnv (.envrc)
export BBAPPROVE_TOKEN_NAME=my-1password-item   # or --token-name / user_token_name; needs the 1Password CLI (op)
```

There is no `--token` option: a token on a command line is visible to every user in the process list and ends up
in shell history.

## Interactive TUI (`-i`)

Lists the same PRs with live status, and acts on them one at a time or in bulk. The TUI stays open
after every action; only `q` / ctrl+c quits.

**Lowercase keys act instantly on the row under the cursor and never touch marks. Uppercase keys act on
the marked rows.** Actions run in the background, so you can mark rows, press `D`, and keep working
(for example `a` on another row) while it runs. A row that is currently working answers "row busy".

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k` | move |
| `a` | approve + merge the cursor row at once (no prompt; refused when not eligible) |
| `d` | decline the cursor row (y/N confirm, then optional comment) |
| `c` | comment on the cursor row (empty comment refused) |
| `i` | ignore the row's dependency (per-user list); on an already ignored row, un-ignore it |
| `o` | open the cursor row's PR in the browser |
| `x` / space | mark / unmark the cursor row |
| `ctrl+a` | mark all eligible rows |
| `u` / ctrl+d | clear marks |
| `A` | approve + merge the marked eligible rows (`approve+merge N PR(s)? y/N`; others skipped, with a count) |
| `D` | decline the marked rows (confirm, then one shared optional comment) |
| `C` | comment on the marked rows (one shared comment, empty refused) |
| `r` | reload the list (cursor and marks are kept by PR URL; per-row result statuses are reset) |
| `.` / `h` | show/hide ignored rows |
| `?` | toggle the extended help |
| `q` | quit; prints a summary of the session to the terminal (see below) |

With no marked rows `A`/`D`/`C` only show "no marked rows". After a batch action the acted rows are un-marked.

### Exit summary

On `q` / ctrl+c the TUI leaves the alternate screen and prints what was done this session, so it stays in the
scrollback: a table in the same fixed-width format as the CLI with only the actions taken (`MERGED`,
`NOT MERGED`, `DECLINED`, `COMMENTED`, `FAIL`, plus `IGNORED` / `UN-IGNORED` rows that show the dependency),
then a tally such as `3 merged, 1 declined, 1 commented, 1 failed, 2 ignored, 1 un-ignored` (zero counts are
omitted). With nothing done it prints `No changes made.` The summary comes from a session log, so a reload
(which resets the row statuses) does not lose anything. Quitting does not wait for running actions: they are
listed as `WORKING…` and the tally ends with `(still running)`. There is one line per PR and kind of action
(a PR that was commented and then merged has two lines). Lines are cut to the terminal width on a TTY and
never truncated when piped; `NO_COLOR` is respected.

### Look and status bar

The cursor row has a subtle full-width background and the `›` marker; marked rows show a bold yellow `▌` bar;
the mouse-hovered row gets a fainter background. With `NO_COLOR` set only the markers remain.
The bottom two lines never wrap: line 1 shows the transient message or active prompt, otherwise the cursor
row's PR URL (where `o` goes), with `bbapprove <version>` dim at the right; line 2 shows `N PRs, M marked`
and as many key hints as fit the terminal width.

### Hints follow the Shift key

By default line 2 shows the instant keys: `a approve  d decline  c comment  i ignore  x mark  o open  ? help  q quit`.
While you HOLD Shift it switches to the batch keys, and releasing Shift restores the instant set:
`A approve marked  D decline marked  C comment marked  ctrl+a mark all  x mark  o open  ? help  q quit`.
Both sets never wrap (trailing items are dropped to fit; `o open` is the last one dropped before `? help` and
`q quit`) and `?` always lists both.

This needs a terminal that speaks the kitty keyboard protocol (Ghostty, kitty, recent iTerm2, WezTerm,
foot, Alacritty): bbapprove requests the enhancements (key release events, all keys as escape codes with
associated text and alternate keys) and detects support from the terminal's reply. In any other terminal
(Terminal.app, tmux without passthrough, ...) the hints simply stay on the instant set, and every key still
works the same. The Shift state is also cleared when the window loses focus or a key arrives without Shift,
in case a release is lost. `BBAPPROVE_KITTY=0` stops bbapprove from requesting the enhancements (hints
never change; Bubble Tea still asks for basic key disambiguation).

### Mouse

Mouse support is on by default: click a row to move the cursor, click the first three columns to toggle its
mark, wheel to scroll (3 rows), hover to highlight. Footer line 1 shows the cursor row's PR URL as a hyperlink,
followed by a dim `o open` hint; click the URL (or the hint) to open it, the same as pressing `o`. On narrow
terminals the URL is shortened in the middle (`https://host/…/pull-requests/4961`) so the PR number and the hint
stay visible. Clicks elsewhere outside the rows are ignored, and the mouse is ignored while a text prompt is open.

Trade-off: capturing the mouse disables the terminal's native text selection and cmd-click on links.
Hold Shift to bypass the capture in most terminals (Ghostty, iTerm2, kitty). Turn it off entirely with
`--no-mouse` or `BBAPPROVE_MOUSE=0`.

Ignore list: `~/.config/bbapprove/ignore` (or `$XDG_CONFIG_HOME/bbapprove/ignore` when `XDG_CONFIG_HOME` is set).
One dependency per line, `#` comments allowed. It is personal and separate from the disallow list in the config:
the disallow list is a repo-wide hard block that always skips a PR, while ignored dependencies are only
hidden from your TUI view (`.` or `h` shows them) and shown dim as `IGNORED` in the CLI (never acted on).

### Columns

Fixed-width columns: `STATUS`, then the check glyphs, then the PR ref and title.

| Column | Glyph meaning |
| --- | --- |
| `STATUS` | the row's action state, 11 characters wide: blank = eligible and untouched; `SKIP` (reason after the title); `IGNORED` (on your ignore list; shown dim); in flight `APPROVING…` `DECLINING…` `COMMENTING…`; done `MERGED` `DECLINED` `COMMENTED`; failures `NOT MERGED` (approved, merge failed) and `FAIL`. A later action on a row replaces its word; `r` resets all of them. A comment never makes a row ineligible. |
| 🔨 build | ✓ passed, ✗ failed, … running, ? unknown / none |
| 🔎 SonarQube | ✓ quality gate passed, ✗ failed, ? unknown |
| 👍 approval | ✓ approved (by someone), ✗ needs work (a reviewer requested changes), – no reviews yet |
| 💬 | comment count (tasks excluded) |
| ✅ | open tasks |
| `PROJECT/repo#id` | PR reference, then the title |

## Environment

| Variable | Purpose |
| --- | --- |
| `BBAPPROVE_TOKEN` | API token; wins over a 1Password item (see [Token](#token)) |
| `BBAPPROVE_CONFIG` | config file path (also `--config`); must exist |
| `BBAPPROVE_REPOSITORY` | forge base URL (overrides `repository`) |
| `BBAPPROVE_TOKEN_NAME` | 1Password item name, used only when `BBAPPROVE_TOKEN` is unset (also `--token-name`; overrides `user_token_name`; no default) |
| `BBAPPROVE_AUTHOR_EMAIL` | author the eligibility check requires (overrides `author_email`) |
| `BBAPPROVE_UPDATE_REPO` | GitHub `owner/repo` for `--outdated` and `--update` (overrides `update_repo`; default `jesperronn/bbapprove`) |
| `BBAPPROVE_DISALLOW` | comma-separated extra disallow items |
| `BBAPPROVE_DISALLOW_FILE` | optional extra disallow file (default `disallow.txt` in the repo root, git-ignored) |
| `BBAPPROVE_JOBS` | parallel PR fetches and merges (default: CPU count) |
| `BBAPPROVE_ADAPTER` | forge adapter executable (default `adapters/bitbucket`) |
| `BBAPPROVE_MOUSE` | `0` turns TUI mouse capture off (same as `--no-mouse`) |
| `BBAPPROVE_KITTY` | `0` stops the TUI requesting the kitty keyboard protocol, so the hints never follow Shift |
| `NO_COLOR`, `FORCE_COLOR` | colour control (colour only on a TTY by default) |

Adapter-side (read by `adapters/bitbucket`; the core sets the first two from the configuration):
`BBAPPROVE_BASE_URL` (required, no default), `BBAPPROVE_TOKEN` (required for `pr` commands, exit 2 when unset), `BBAPPROVE_TOKEN_NAME` (1Password item for `auth token`; no default),
`BBAPPROVE_MERGE_RETRIES` (default 4; merge retries when the PR version went stale), `BBAPPROVE_RETRY_SLEEP`
(default 2 seconds between retries).

## Architecture

The Go code never talks to the forge directly. All forge access goes through an adapter, a `gh`-style CLI
(`BBAPPROVE_ADAPTER`), run as a subprocess with the token in `BBAPPROVE_TOKEN`:

```
adapter auth token
adapter pr list --reviewer @me --state open   # JSON array of normalized PRs
adapter pr view URL                           # one normalized PR
adapter pr review URL --approve
adapter pr merge URL                          # retries stale versions itself
adapter pr comment URL --body TEXT            # or --body-file FILE; empty body exits 2
adapter pr close URL [--comment TEXT]         # decline; --comment is posted first, and a failed comment aborts the decline
```

Normalized PR JSON: `{url, number, project, repo, title, author:{email,login}, headBranch, approvedByMe,
build: passed|failed|running|none, qualityGate: passed|failed|unknown, approvals, needsWork, commentCount, openTasks}`.
A GitHub/GitLab adapter only needs to implement these commands and emit this shape.

Packages: `internal/adapter` (subprocess client), `internal/rules` (eligibility), `internal/app` (CLI and
dry run/apply), `internal/tui` (Bubble Tea interface), `internal/ignore` (per-user ignore list).
More in [docs/architecture.md](docs/architecture.md).

## Build, install, version

```
bin/build                      # build bin/bbapprove-go with the version embedded
bin/build --test               # gofmt -l, go vet, go test -race first; abort on failure
bin/build --install            # build, then symlink bin/bbapprove to ~/.local/bin/bbapprove
bin/build --install --prefix DIR [--force]
bin/build --version            # print the version that would be embedded (no build)
bin/build -o FILE              # write the binary somewhere else
bbapprove --version            # prints "bbapprove <version>"; -V works too
```

`--install` links the launcher `bin/bbapprove`, not the Go binary, because the launcher keeps working from
the repo (adapters/ and bbapprove.config.toml are resolved relative to it) and rebuilds when the sources change.
The prefix defaults to `$HOME/.local/bin` (created if missing). An existing link to this launcher is left
alone; any other file at the target is refused unless `--force`. After installing, `bin/build` warns when
`command -v bbapprove` resolves elsewhere. Another `bbapprove` earlier on PATH would shadow the
installed link; the warning names the winner.

Versioning comes from git: `git describe --tags --always --dirty` (for example `v1.0.0-rc.1`,
`v1.0.0-rc.1-3-gabc1234`, `v1.0.0-rc.1-dirty`), injected with `-ldflags "-X main.version=..."` and shown
exactly as git prints it. Without git a build reports `dev`; `BBAPPROVE_VERSION` overrides the value (tests,
release builds). The version also appears on the first line of `--help` and dim in the TUI footer.

Maintainers cut releases with `bin/release X.Y.Z`; see [docs/releasing.md](docs/releasing.md).

## CI and releases

GitHub Actions (`.github/workflows/`; details in [docs/ci.md](docs/ci.md)):

- **CI** (`ci.yml`) runs on pushes to `main`, pull requests and manually. On `ubuntu-latest` and `macos-latest` it
  runs `bin/lint` and `bin/test` (Go tests with `-race` plus the shell suites), with Go taken from `go.mod`.
  The shell suites need bash 4+ and ripgrep, so the macOS job installs Homebrew bash, ripgrep and shellcheck.
- **Release** (`release.yml`) runs when a `v*` tag is pushed: it reruns CI, cross-builds with `bin/build`
  (darwin arm64/amd64, linux amd64/arm64), uploads one `.tar.gz` per platform plus `SHA256SUMS`, and publishes the
  GitHub Release with the matching CHANGELOG.md section as notes (`bin/release-notes <tag>`). Tags containing
  `-` (for example `v1.0.0-rc.3`) are marked pre-release.
- **Dependabot** (`dependabot.yml`) opens a weekly (Monday) PR per ecosystem (`gomod`, `github-actions`) that groups
  minor and patch updates, titled `chore(deps): ...` and labelled `dependencies`, so bbapprove's own eligibility
  rule recognises them. Majors arrive as separate PRs for a human. Actions are pinned by commit SHA; Dependabot
  keeps them current.

Install from a release archive: each `bbapprove-<tag>-<os>-<arch>.tar.gz` is a self-contained tree
(`bin/bbapprove` launcher, `bin/bbapprove-go` binary, `adapters/bitbucket`, `bbapprove.config.template`, docs). Verify it
against `SHA256SUMS`, extract it somewhere permanent and link the launcher onto PATH:

```
tar -xzf bbapprove-v1.0.0-darwin-arm64.tar.gz -C ~/.local/share
ln -s ~/.local/share/bbapprove-v1.0.0-darwin-arm64/bin/bbapprove ~/.local/bin/bbapprove
```

The binary finds `adapters/` and `bbapprove.config.toml` relative to its own directory's parent, so keep the tree intact. After extracting, create your config: `cp bbapprove.config.template bbapprove.config.toml` in the extracted directory (or put it in `~/.config/bbapprove/config.toml`).

## Maintainer scripts

Every script takes `--help`; `bin/help` lists them. Details in [docs/development.md](docs/development.md).

| Script | Purpose |
| --- | --- |
| `bin/setup` | check tools, download modules, build (once per clone) |
| `bin/test` | Go and shell test suites (`--verbose`, `--no-go`, `--no-shell`, or a target) |
| `bin/lint` | gofmt, go vet, shellcheck (`--autofix`, or pass paths) |
| `bin/build` | build `bin/bbapprove-go` with the version embedded |
| `bin/install` | build and link the launcher into `~/.local/bin` |
| `bin/release X.Y.Z` | changelog, tag, push, watch the release workflow (`--dry-run` first) |
| `bin/release-notes TAG` | print the CHANGELOG section for a tag |

## Contributing

Start with [AGENTS.md](AGENTS.md) (rules, definition of done) and the maintainer docs in [docs/](docs/README.md):
[development](docs/development.md), [architecture](docs/architecture.md), [releasing](docs/releasing.md), [CI](docs/ci.md).
Open work is in [TODO.md](TODO.md).
