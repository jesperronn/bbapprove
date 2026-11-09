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
adapter auth token                                  # prints the token (BBAPPROVE_TOKEN or the password manager)
adapter pr list --reviewer @me --state open         # JSON array of normalized PRs
adapter pr view URL | pr review URL --approve | pr merge URL
adapter pr comment URL --body TEXT | --body-file FILE
adapter pr close URL [--comment TEXT]               # decline
```

Normalized PR: `{url, number, project, repo, title, author:{email,login}, headBranch, approvedByMe, build: passed|failed|running|none, qualityGate: passed|failed|unknown, approvals, needsWork, commentCount, openTasks}`. Errors go to stderr with a non-zero exit.

## Classification

`rules.Classify` is the one function behind CLI and TUI. Order:
1. ignore list match: `IGNORED` (wins over everything, never acted on)
2. author is not `author_email`
3. subject does not start `chore(deps):` / `fix(deps):`
4. title matches a `disallow` entry
5. major update (title ends `to vN` or branch `renovate/major-*`; heuristic, see [../TODO.md](../TODO.md))
6. already approved by me
Otherwise eligible. Steps 2-6 give `SKIP` with a reason.

Two lists: `disallow` (config file plus `--disallow`/`BBAPPROVE_DISALLOW`) is a repo-wide hard block shown as `SKIP`; the ignore file (`$XDG_CONFIG_HOME/bbapprove/ignore`, default `~/.config/bbapprove/ignore`) is personal, edited with `i` in the TUI, and shown as `IGNORED`.

## Config

Per setting: flag > environment (`BBAPPROVE_*`) > file > default. File lookup: `--config`, `$BBAPPROVE_CONFIG`, `<repo root>/bbapprove.config.toml`, `$XDG_CONFIG_HOME/bbapprove/config.toml`. Only `type` and `user_token_name` have defaults. The adapter never reads TOML. First-run creation and `--init` write the embedded template (`internal/config/setup.go`).

## Adding a forge adapter

1. Create `adapters/<type>` (executable, `# shellcheck shell=bash` if bash) implementing the contract above; it is selected by `type = "<type>"` (or `BBAPPROVE_ADAPTER`).
2. Read `BBAPPROVE_BASE_URL`, `BBAPPROVE_TOKEN_NAME`, `BBAPPROVE_TOKEN`; fail with a message and non-zero exit when the base URL is unset (exit 2).
3. Add `test/<type>-adapter.test.sh` modelled on `test/bitbucket-adapter.test.sh`: stub `curl` on `PATH`, check each command's JSON shape and error paths.
4. Add the file to the release archive step in `.github/workflows/release.yml` and mention it in README and CHANGELOG.

## Updates (`internal/update`)

`--outdated` and `--update` run through a `Runner` interface (`gh`, `git`, `bin/build`; no shell, `os/exec` only), so
tests inject a fake. A checkout is updated in place; any other install gets a verified release unpacked side by side
into a sibling directory, and the running tree is never touched. Archive extraction refuses absolute paths, `..`,
links that leave the release directory, devices and other special files, and stops at 200 MiB or 50,000 entries.
setuid, setgid and sticky bits are dropped. Details and the limits of `SHA256SUMS`: [README](../README.md#updating).

## Diagnostics (`internal/doctor`)

`bbapprove doctor` is a read-only report (version, update status, config candidates and the one in use, setting
sources via `config.Settings`, adapter, token sources, ignore and disallow lists, tools). Every process seam
(environment, `LookPath`, executable, the `update.Runner`) is injected via `doctor.Options`; the real wiring is
`app.runDoctor`. It never prints a token and never runs `op`. Exit 1 when any FAIL was found.

## Colour (`internal/style`)

One shared package decides whether to colour (`style.Enabled`: `NO_COLOR` wins, else a TTY or `FORCE_COLOR`) and maps token
classes to 16-colour SGR codes (`Theme`). Help texts are written as `{class:text}` markup and rendered by it; doctor and
`--print-config` paint through the same `Theme`, so plain output (no colour) equals the markup with the tags removed.

## Safety principles

- Dry run by default; only `--apply` (or an explicit TUI key and prompt) changes anything.
- Ignored PRs are never approved, merged, declined or commented.
- Fail closed: an unreadable ignore file stops the run.
- Decline and batch actions ask for confirmation; empty comments are refused.
- No secrets in logs or `--print-config`; the core hands the token to the adapter via `BBAPPROVE_TOKEN`.
- The config lives outside the tree (`~/.config/bbapprove`), mode 0600, and is never overwritten without `--force`.
