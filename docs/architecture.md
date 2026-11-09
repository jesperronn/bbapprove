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

`pr merge` retries stale PR versions itself. The core sets `BBAPPROVE_BASE_URL` (required by the adapter, exit 2 when unset), `BBAPPROVE_TOKEN_NAME` and `BBAPPROVE_DELETE_SOURCE_BRANCH` from the configuration; `BBAPPROVE_TOKEN` is required by the `pr` commands (exit 2 when unset). Adapter tunables (`BBAPPROVE_MERGE_RETRIES`, `BBAPPROVE_RETRY_SLEEP`): `bbapprove help env`.

Normalized PR: `{url, number, project, repo, title, author:{email,login}, headBranch, approvedByMe, build: passed|failed|running|none, qualityGate: passed|failed|unknown, approvals, needsWork, commentCount, openTasks}`. Errors go to stderr with a non-zero exit.

## The Go Bitbucket adapter (`internal/forge/bitbucket`)

A native Go port of `adapters/bitbucket`, so a Windows build needs no bash, curl or jq. It is a library (`Client` with `ListPRs`, `ViewPR`, `Approve`, `Merge` (and `MergeWarn`), `Comment`, `Close`, plus `AuthToken`) and a `Run(ctx, args, env, stdout, stderr)` entry that speaks the same CLI protocol (exit 0/1/2, same commands, same normalized JSON with the same key order). It uses only the standard library (`net/http`, `encoding/json`, `os/exec` for `op`). The bearer token goes only into the `Authorization` header and is never printed. Every request has a 2.7 s connect and 3.5 s total timeout, one connection per request, and redirects are not followed. It is not wired into the core yet: the core still runs the bash adapter.

`cmd/bbapprove-bitbucket` is a thin `main` around `Run`. It exists only so the tests can run the Go adapter as a process; the release builds `./cmd/bbapprove` alone, so it is never shipped.

### How it is tested against the bash adapter

`internal/bbfake/adapter_char_test.go` runs every case twice: against the real bash script and against the Go binary (built once in `TestMain`). The goldens in `internal/bbfake/testdata/adapter` pin exit code, stdout, stderr (after redactions) and the recorded requests. The Go run must equal `<case>.golden.json` exactly. A case that differs on purpose carries `goDiff` (the decision numbers below) and a separate `<case>.go.golden.json`; `goOnly` cases exercise Go behaviour that has no bash counterpart. `TestGoGoldensHaveCases` fails on a Go golden without such a case, and a `goDiff` whose Go output equals the bash golden fails too, so every difference is explicit. The bash side skips when bash, curl or jq is missing; the Go side always runs. Refresh with `go test ./internal/bbfake -run Char -update`.

### Deliberate differences from the bash adapter

1. The token is never on a command line (curl's argv exposed it); no golden shows a difference because the tests do not read `ps`.
2. A missing URL argument on `view`, `merge`, `review`, `comment` and `close` is one usage error (`pr <cmd>: missing PR URL`), exit 2, instead of bash's `unbound variable`.
3. `pr list` accepts only `--reviewer @me --state open` (either order) and every command rejects unknown or extra arguments with exit 2; bash ignored them.
4. Dashboard paging stops with an error when `isLastPage` is false and there is no `nextPageStart` greater than the current start; bash looped on `null`.
5. A 200 reply that is not JSON gives `unexpected response from <endpoint>: not JSON`, exit 1 (also for the stats call and the version read of merge and close); bash printed a jq error. The activity and blocker-comment calls stay soft, as in bash.
6. Activities and blocker comments are paged (`isLastPage` / `nextPageStart`, at most 50 pages of 100) so `commentCount` and `openTasks` are right on busy PRs. A failed page makes that count 0, as a failed call did in bash; a page without a usable `nextPageStart`, or the 50-page cap, ends the paging with what was counted.
7. Trailing slashes on `BBAPPROVE_BASE_URL` are trimmed; bash produced `//projects` and a 404.
8. `auth token` does not need `BBAPPROVE_BASE_URL`.
9. A transport failure prints one clean line, `<METHOD> <url path>: connection failed` or `timed out after 3.5s` (2.7 s when connecting), instead of curl's text plus an `HTTP 000` line. A failed stats call is silent.
10. Merge and decline bodies are built from an integer version, never by pasting server text into JSON.
11. `BBAPPROVE_MERGE_RETRIES` and `BBAPPROVE_RETRY_SLEEP` must be non-negative integers, else exit 2 with a message (checked by `merge` and `close`).
12. A 3xx reply is reported as `redirect to <Location>; check BBAPPROVE_BASE_URL / repository`, not as a bare HTTP code, for the commands that fail on a bad status. Whoami and stats stay soft.
12b. `pr close` retries a declined 409 `out-of-date` like `pr merge` (same attempts and sleep, version re-read each time); a comment posted first stays posted. The whoami reply is trimmed at both ends only (bash deleted all whitespace).

One more difference follows from dropping `cat`: a missing `--body-file` reports `pr comment: cannot read FILE: no such file or directory` without cat's own line. Fields the server leaves out (a null title, project key or slug) become empty strings where jq printed `null`; a real server always sends them.

Kept as in bash, on purpose: stats in one POST with every sha; dashboard page limit 100 and no page cap; merge retries only on 409 `out-of-date` (never on 5xx or timeouts) and do not re-check mergeability; `close --comment` posts the comment first and a failed comment aborts the decline; `--body-file -` reads stdin; the dim `Waiting for 1Password` notice and its `NO_COLOR` / `FORCE_COLOR` rules; the `auth token` order (`BBAPPROVE_TOKEN`, then the `token` field, then `credential`).

Undecided, kept as in bash: `pr view` reports `qualityGate: unknown` because the single-PR GET carries no `qgStatus`, while `pr list` reports the real value. This needs a check against a real server (a TODO in `ViewPR`).

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

### Deleting the source branch after a merge

The `delete_source_branch` setting (default true) reaches the adapter as the environment variable `BBAPPROVE_DELETE_SOURCE_BRANCH=true|false`, not as a command-line flag, so an external adapter that has never heard of it keeps working. A hand-run adapter treats an unset variable as true and rejects any value other than true, false, 1, 0, yes or no with exit 2 before it touches the forge.

After the merge call has succeeded, `adapters/bitbucket` and the Go adapter delete the PR's source branch with `DELETE /rest/branch-utils/1.0/projects/KEY/repos/SLUG/branches` and the JSON body `{"name":"refs/heads/<branch>","dryRun":false}`. It does so once, after the successful attempt (the stale-version retry loop is unchanged), and only when the source branch lives in the same repository as the target, the source ref is a branch, and it is not the target branch itself. A fork's branch is never deleted. An HTTP 404 means the branch is already gone and is silent. Any other failure, including a dropped connection, leaves the merge successful (exit 0) and prints `merge ok, but could not delete branch <name> (HTTP <code>)` on stderr. A failed merge never deletes anything. The call needs repository write permission for the token. The Go adapter reads the variable into `Env.DeleteSourceBranch`; with a dropped connection it also prints its one-line transport error before the warning (decision 9).

## Adding a forge adapter

1. Create `adapters/<type>` (executable, `# shellcheck shell=bash` if bash) implementing the contract above; it is selected by `type = "<type>"` (or `BBAPPROVE_ADAPTER`).
2. Read `BBAPPROVE_BASE_URL`, `BBAPPROVE_TOKEN_NAME`, `BBAPPROVE_TOKEN` and, if the forge can delete branches, `BBAPPROVE_DELETE_SOURCE_BRANCH`; exit 2 with a message when the base URL is unset, or (for `pr` commands) when `BBAPPROVE_TOKEN` is unset; `auth token` may run without it.
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
