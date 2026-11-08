# bbapprove

Approve and merge Renovate PRs on Bitbucket Server (bitbucket.example.com) via the REST API.
Dry-run by default; `--apply` approves, then merges.

The core is a Go program (`cmd/bbapprove`, `internal/`). `bin/bbapprove` is a small bash launcher:
it (through `bin/build`) builds `bin/bbapprove-go` when it is missing or older than the Go sources (`go.mod`, `go.sum`,
`cmd/**/*.go`, `internal/**/*.go`) and then execs it with all arguments unchanged. Building needs Go
on PATH (https://go.dev/dl/); without it and without a built binary the launcher exits 127.

## Usage

```
bin/bbapprove                      # dry run: list PRs where I'm reviewer, marked ELIGIBLE or SKIP (reason)
bin/bbapprove --apply              # approve + merge the eligible ones
bin/bbapprove --apply <PR_URL>...  # only the given PRs (PR URLs skip the reviewer list)
bin/bbapprove -i                   # interactive TUI (see below)
bin/bbapprove --help               # flags, eligibility rules, env vars
```

Flags: `--apply`, `--disallow a,b` (adds to the disallow list), `--token T`, `-i/--interactive`, `-h/--help`.

Eligible, all of:
- author is `BBAPPROVE_AUTHOR_EMAIL` (default `renovate-bot@example.com`)
- subject starts `chore(deps):` or `fix(deps):`
- not on the disallow list (`disallow.txt` in the repo root, one dependency per line, `#` comments and blank lines ignored; currently `some-library` and `another-library`; plus `--disallow` / `BBAPPROVE_DISALLOW`). Case-insensitive substring match on the title.
- minor/patch/digest only. Current heuristic: a title ending `to vN` or a branch `renovate/major-*` counts as major. This is not reliable yet, see TODO.md.
- not already approved by you

Anything else is reported as SKIP with the reason. Nothing is changed without `--apply`.

## Interactive TUI (`-i`)

Lists the same PRs with live status, and acts on them one at a time or in bulk.

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k` | move |
| `x` / space | mark / unmark the current row |
| `X` | mark all eligible rows |
| `u` / ctrl+d | clear marks |
| `a` | approve + merge the marked rows, else the current row |
| `d` | decline (confirm, then optional comment) |
| `c` | comment on the marked rows, else the current row |
| `i` | ignore the row's dependency (per-user list); on an already ignored row, un-ignore it |
| `.` | show/hide ignored rows |
| `o` | open the PR in the browser |
| `?` | toggle the longer help |
| `q` | quit |

Ignore list: `~/.config/bbapprove/ignore` (or `$XDG_CONFIG_HOME/bbapprove/ignore` when `XDG_CONFIG_HOME` is set).
One dependency per line, `#` comments allowed. It is personal and separate from `disallow.txt`:
the disallow list is a repo-wide hard block that always skips a PR, while ignored dependencies are only
hidden from your TUI view.

### Columns

Fixed-width columns: `STATUS`, then the check glyphs, then the PR ref and title.

| Column | Glyph meaning |
| --- | --- |
| `STATUS` | `ELIGIBLE`, `SKIP` (with reason), `IGNORED`, or the result of an action (`MERGED`, `APPROVED, NOT MERGED`, `DECLINED`, `FAIL`) |
| 🔨 build | ✓ passed, ✗ failed, … running, ? unknown / none |
| 🔎 SonarQube | ✓ quality gate passed, ✗ failed, ? unknown |
| 👍 approval | ✓ approved (by someone), ✗ needs work (a reviewer requested changes), – no reviews yet |
| 💬 | comment count (tasks excluded) |
| ✅ | open tasks |
| `PROJECT/repo#id` | PR reference, then the title |

## Environment

| Variable | Purpose |
| --- | --- |
| `BBAPPROVE_TOKEN` | API token (also `--token`); the adapter's `auth token` prints it when unset |
| `BBAPPROVE_AUTHOR_EMAIL` | author the eligibility check requires |
| `BBAPPROVE_DISALLOW` | comma-separated extra disallow items |
| `BBAPPROVE_DISALLOW_FILE` | disallow file instead of `disallow.txt` |
| `BBAPPROVE_JOBS` | parallel PR fetches and merges (default: CPU count) |
| `BBAPPROVE_ADAPTER` | forge adapter executable (default `adapters/bitbucket`) |
| `NO_COLOR`, `FORCE_COLOR` | colour control (colour only on a TTY by default) |

Adapter-only (read by `adapters/bitbucket`): `BBAPPROVE_BASE_URL` (default `https://bitbucket.example.com`),
`BBAPPROVE_MERGE_RETRIES` (default 4; merge retries when the PR version went stale), `BBAPPROVE_RETRY_SLEEP`
(default 2 seconds between retries).

The dotfiles wrapper (`dotfiles/bin/bbapprove`) execs `bin/bbapprove` and injects `BBAPPROVE_TOKEN` from
1Password; all flags pass through unchanged.

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
the repo (adapters/ and disallow.txt are resolved relative to it) and rebuilds when the sources change.
The prefix defaults to `$HOME/.local/bin` (created if missing). An existing link to this launcher is left
alone; any other file at the target is refused unless `--force`. After installing, `bin/build` warns when
`command -v bbapprove` resolves elsewhere. Note the dotfiles wrapper `dotfiles/bin/bbapprove` (see
Environment) may come earlier on PATH and shadow the installed link; the warning names the winner.

Versioning comes from git: `git describe --tags --always --dirty` (for example `v1.0.0-rc.1`,
`v1.0.0-rc.1-3-gabc1234`, `v1.0.0-rc.1-dirty`), injected with `-ldflags "-X main.version=..."` and shown
exactly as git prints it. Without git a build reports `dev`; `BBAPPROVE_VERSION` overrides the value (tests,
release builds). The version also appears on the first line of `--help` and dim in the TUI footer.

Cut a release: update CHANGELOG.md, commit, then

```
git tag -a v1.0.0 -m "bbapprove 1.0.0"
bin/build --test --install
```

## Test and lint

```
go vet ./... && go test ./... -race          # Go unit tests
bash test/bitbucket-adapter.test.sh          # adapter against a stubbed curl
bash test/bbapprove-launcher.test.sh         # launcher: rebuild rules, arg passing, symlinks, missing go
bash test/build.test.sh                      # bin/build: version embedding, --install, --test, missing go
shellcheck bin/bbapprove bin/build adapters/bitbucket test/*.sh
gofmt -l .                                   # should print nothing
```
