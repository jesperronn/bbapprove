# Development

Run `bin/help` for the commands; every one takes `--help`. Start with `bin/setup`, then `bin/lint` and `bin/test` must be green. Rules for contributors: [../AGENTS.md](../AGENTS.md).

The `bin/` commands colour their help like bbapprove (`bin/lib/style.sh`, `internal/style`): on for a terminal or `FORCE_COLOR`, off with `NO_COLOR`.

```
bin/test test/release.test.sh     # one shell suite
bin/test ./internal/tui           # one Go package
bin/test --no-go 2>&1 | grep -E 'FAIL|TEST SUMMARY'
bin/test --timings                # after the run: slowest suites, Go packages and Go tests, total time
```

`bin/lint [--autofix] [PATH...]` runs shellcheck and shfmt on shell scripts and gofmt and go vet on Go. shfmt takes two-space indentation and indented `case` branches from `.editorconfig`; it is optional (skipped with a note when not installed, `brew install shfmt`), and `--autofix` rewrites formatting with `gofmt -w` and `shfmt -w`.

`bin/lint-commits [--title TEXT] [--body-file FILE] [RANGE]` checks commit messages (default `origin/main..HEAD`) and PR text against the message policy in AGENTS.md; run it before pushing.

The launcher `bin/bbapprove` rebuilds `bin/bbapprove-go` whenever `go.mod`, `go.sum` or any `cmd/`/`internal/` `.go` file is newer than it, so edit, run `bin/bbapprove`, repeat. Without Go and without a binary it exits 127.
`bin/install` (or `bin/build --install`) links the launcher, not the Go binary, into the prefix (default `~/.local/bin`) because the launcher resolves `adapters/` and `bbapprove.config.toml` relative to the repo and rebuilds on change. An existing link to this launcher is left alone, any other file at the target is refused unless `--force`, and the build warns when another `bbapprove` earlier on `PATH` shadows the link.

## Layout

```
cmd/bbapprove/       main: wires os.Args, env, stdio into internal/app
internal/app/        CLI: flag parsing, dry run / --apply, table rendering, --help
internal/config/     TOML config, lookup and precedence, first-run setup and --init, embedded template
internal/rules/      eligibility (Config.Check) and Classify (shared by CLI and TUI)
internal/ignore/     per-user ignore list file
internal/doctor/     `bbapprove doctor` diagnostics (all process seams injected)
internal/adapter/    subprocess client for the forge adapter CLI
internal/bbfake/     fake Bitbucket Server and adapter characterization tests (test support only)
internal/forge/      native Go forge adapters (bitbucket); cmd/bbapprove-bitbucket is its test-only executable
internal/tui/        Bubble Tea interface (-i)
adapters/bitbucket   bash adapter: the only code that knows curl and Bitbucket
bin/                 launcher, build/test/lint/setup/install/release scripts, bin/help
test/                *.test.sh suites; test/lib/bash_test.sh is the shared harness
.github/             workflows (ci, release) and dependabot.yml
```

## Test conventions

- Hermetic: no network, no real forge, no `op`, no real `~/.config`. Point `HOME`/`XDG_CONFIG_HOME` at a temp dir (`testfix.SetHome` also sets `USERPROFILE` and `APPDATA` on Windows).
- Shell suites source `test/lib/bash_test.sh` (`test_pass`, `assert_eq`, `assert_contains`, `assert_status`, `run_tests`). Put stubs for `curl`, `gh`, `git` etc. in a temp dir prepended to `PATH`. `make_tool_dir DIR [TOOL...]` builds a minimal `PATH` that reliably lacks go, shellcheck, etc. Adapter-facing Go tests use fake adapter scripts.
- Stub fixtures: never write a stub script per test (a freshly written executable costs 45-200 ms on its first run on macOS, a symlink to an already-run one costs nothing). The fixed, env-driven stubs live in `test/fixtures` (`git`, `gh`, `curl`, `op`, `go` passthrough, `go-stub`, `recorder`, `noop`, `fake-suite`); each documents its env switches in its header. Link them with `use_stubs DIR NAME[=AS]...` (for example `use_stubs "$tools" recorder=shellcheck go-stub=go`), put DIR on `PATH`, and point the log at the test's own temp dir with `STUB_LOG`. `link_script SRC DEST` hardlinks the script under test into a fake repo instead of copying it. To add a stub: add an executable `test/fixtures/NAME` that reads all behaviour from env vars or a data file the test points it at (no per-test state in the fixture), document the switches in its header, and use `use_stubs`. A script a test writes itself must be truly unique to that test.
- Go code takes injected seams (TTY check, editor runner, env lookup, stdin/stdout) instead of touching the real terminal or environment.
- Make waits configurable env vars and set them to `0` in tests (for example `BBAPPROVE_RELEASE_RUN_WAIT_SECONDS`).
- Budget about 1-2 s per suite. Every assertion prints a `[PASS]`; a silent conditional is not a test.
- `internal/bbfake` is a fake Bitbucket Server (httptest) with scenario data, request recording and failure injection; it is test support only. `internal/bbfake/adapter_char_test.go` runs the real `adapters/bitbucket` against it for every subcommand and pins exit code, stdout, stderr and the requests sent in `internal/bbfake/testdata/adapter/*.golden.json` (refresh: `go test ./internal/bbfake -run Char -update`); the bash side skips when bash, curl or jq is missing. The same cases also run against the native Go adapter (`internal/forge/bitbucket`, built as `cmd/bbapprove-bitbucket` in `TestMain`), which must equal the bash golden unless a case declares `goDiff` with its own `.go.golden.json`; the differences are listed in [architecture.md](architecture.md).
- Help and docs must not drift: `internal/app/docs_test.go` fails when the README and `bbapprove help` disagree about flags or environment variables. Help texts are golden-tested; refresh with `go test ./internal/app -run Golden -update`.
- Real-terminal checks (TUI, prompts): run in a tmux session and send keys ONE AT A TIME with a short sleep between them; batched keys race the renderer. Wait for a marker in `tmux capture-pane` output before asserting.

## Finding slow tests

`bin/test --timings` prints the slowest shell suites, Go packages, shell tests and Go tests (`TEST_TIMINGS_TOP`, default 10), then an `Over budget` list. The budgets are informational and never change the exit code: `TEST_SLOW_SUITE_SECONDS` (default 3) applies to shell suites and Go packages, `TEST_SLOW_TEST_SECONDS` (default 1) to single shell and Go tests. Shell tests are timed by `test/lib/bash_test.sh`, which appends `SECONDS<TAB>SUITE<TAB>TEST` lines to the file named by `BASH_TEST_TIMING_FILE` when that is set (`bin/test` sets it for you).

A nightly agent should run `bin/test --timings`, read the `Over budget` list (every entry over a budget, slowest first, not just the top N) and fix the slowest first: stub slow external calls, make waits configurable env vars set to `0` in tests, or share fixtures. Never delete assertions to gain speed. Re-run and confirm the entry is gone from the list and `bin/test` still passes.

## Adding a `bin/` script

1. `#!/usr/bin/env bash`, `# shellcheck shell=bash`, then line 3 exactly `# <name> — <description>`. `bin/help` scrapes that line.
2. `usage()` for `-h`/`--help` (exit 0); unknown option exits 2. Exit codes: 0 ok, 1 failure, 2 usage error, 127 missing tool.
3. `chmod +x`, and add `test/<name>.test.sh` (copy the shape of `test/release-notes.test.sh`).
4. Run `bin/lint bin/<name> test/<name>.test.sh`, `bin/test test/<name>.test.sh` and `bin/test test/help.test.sh`, then add a CHANGELOG line.
