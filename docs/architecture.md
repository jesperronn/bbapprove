# Architecture

How the pieces fit, the rules that classify a PR, and how to add a forge. Package map: [development.md](development.md#layout).

```
bin/bbapprove (launcher: rebuild if stale, exec)
      |
bin/bbapprove-go  =  cmd/bbapprove -> internal/app
      |   config (internal/config)  rules (internal/rules)  ignore list (internal/ignore)
      |   CLI table / TUI (internal/tui)
      v   subprocess, token in BBAPPROVE_TOKEN, BBAPPROVE_BASE_URL / _TOKEN_NAME in env
adapters/<type>   (gh-style CLI, internal/adapter is the client)
      v
forge REST API
```

## Data flow

The app resolves the config, runs `adapter pr list --reviewer @me --state open` (or `pr view URL` for given PR URLs), classifies each PR, and prints a table. With `--apply` it runs `pr review URL --approve` then `pr merge URL` for eligible PRs only, in parallel (`BBAPPROVE_JOBS`). The TUI shows the same list and acts on request.

## Adapter contract

```
adapter auth token                                  # prints the token from the 1Password item BBAPPROVE_TOKEN_NAME; the core runs it only when BBAPPROVE_TOKEN is empty and a name is set (order: `bbapprove help token`)
adapter pr list --reviewer @me --state open         # JSON array of normalized PRs
adapter pr view URL | pr review URL --approve | pr merge URL
adapter pr comment URL --body TEXT | --body-file FILE   # an empty body exits 2
adapter pr close URL [--comment TEXT]               # decline; the comment is posted first and a failed comment aborts the decline
```

`pr merge` retries stale PR versions itself. The core sets `BBAPPROVE_BASE_URL` (required by the adapter, exit 2 when unset) and `BBAPPROVE_TOKEN_NAME` from the configuration; `BBAPPROVE_TOKEN` is required by the `pr` commands (exit 2 when unset). Adapter tunables (`BBAPPROVE_MERGE_RETRIES`, `BBAPPROVE_RETRY_SLEEP`): `bbapprove help env`.

Normalized PR: `{url, number, project, repo, title, author:{email,login}, headBranch, approvedByMe, build: passed|failed|running|none, qualityGate: passed|failed|unknown, approvals, needsWork, commentCount, openTasks}`. Errors go to stderr with a non-zero exit.

## Classification

`rules.Classify` is the one function behind CLI and TUI (`internal/rules`). Order:
1. ignore list match: `IGNORED` (wins over everything, never acted on)
2. author is not `author_email`
3. subject does not start `chore(deps):` / `fix(deps):`
4. title matches a `disallow` entry
5. major update (title ends `to vN` or branch `renovate/major-*`; heuristic, see [../TODO.md](../TODO.md))
6. already approved by me
Otherwise eligible. Steps 2-6 give `SKIP` with a reason.

What each rule means for users, and the difference between the `disallow` and ignore lists: `bbapprove help rules`. The CLI footer, the table and the TUI all take their result from this one function.

## Config

Lookup, precedence and defaults: `bbapprove help config`. The adapter never reads TOML: the core hands it resolved values. First-run creation and `--init` write the embedded template (`internal/config/setup.go`); `internal/config/bbapprove.config.template` must stay byte-identical to the root template.

## Adding a forge adapter

1. Create `adapters/<type>` (executable, `# shellcheck shell=bash` if bash) implementing the contract above; it is selected by `type = "<type>"` (or `BBAPPROVE_ADAPTER`).
2. Read `BBAPPROVE_BASE_URL`, `BBAPPROVE_TOKEN_NAME`, `BBAPPROVE_TOKEN`; exit 2 with a message when the base URL is unset, or (for `pr` commands) when `BBAPPROVE_TOKEN` is unset; `auth token` may run without it.
3. Add `test/<type>-adapter.test.sh` modelled on `test/bitbucket-adapter.test.sh`: stub `curl` on `PATH`, check each command's JSON shape and error paths.
4. Add the file to the release archive step in `.github/workflows/release.yml` and mention it in README and CHANGELOG.

## Updates (`internal/update`)

`--outdated` and `--update` run through a `Runner` interface (`gh`, `git`, `bin/build`; no shell, `os/exec` only), so
tests inject a fake. A checkout is updated in place; any other install gets a verified release unpacked side by side
into a sibling directory, and the running tree is never touched. Archive extraction refuses absolute paths, `..`,
links that leave the release directory, devices and other special files, and stops at 200 MiB or 50,000 entries.
setuid, setgid and sticky bits are dropped. Behaviour and the limits of `SHA256SUMS`: `bbapprove help update`.

## Diagnostics (`internal/doctor`)

`bbapprove doctor` is a read-only report (version, update status, config candidates and the one in use, setting
sources via `config.Settings`, adapter, the token source (BBAPPROVE_TOKEN set / 1Password item name; no value), ignore and disallow lists, tools). Every process seam
(environment, `LookPath`, executable, the `update.Runner`) is injected via `doctor.Options`; the real wiring is
`app.runDoctor`. It never prints a token and never runs `op`. Exit 1 when any FAIL was found.

## Colour (`internal/style`)

One shared package decides whether to colour (`style.Enabled`: `NO_COLOR` wins, else a TTY or `FORCE_COLOR`) and maps token
classes to 16-colour SGR codes (`Theme`), so output follows the terminal theme: headings bold cyan, subcommands bold magenta, flags green, placeholders bright blue, environment variables yellow, paths cyan, notes dim, `ok`/`warn`/`FAIL` green/yellow/bold red, and the sources `[flag]` `[env]` `[file]` `[default]` magenta/yellow/green/dim. Help texts are written as `{class:text}` markup and rendered by it; doctor and
`--print-config` paint through the same `Theme`, so plain output (no colour) equals the markup with the tags removed.

## Safety principles

- Dry run by default; only `--apply` (or an explicit TUI key and prompt) changes anything.
- Ignored PRs are never approved, merged, declined or commented.
- Fail closed: an unreadable ignore file stops the run.
- Decline and batch actions ask for confirmation; empty comments are refused.
- The token never appears on a command line (no `--token` flag); with no token source the core exits 1 before any adapter call.
- No secrets in logs or `--print-config`; the core hands the token to the adapter via `BBAPPROVE_TOKEN`.
- The config lives outside the tree (`~/.config/bbapprove`), mode 0600, and is never overwritten without `--force`.
