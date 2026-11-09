# Development

Run `bin/help` for the commands; every one takes `--help`. Start with `bin/setup`, then `bin/lint` and `bin/test` must be green. Rules for contributors: [../AGENTS.md](../AGENTS.md).

```
bin/test test/release.test.sh     # one shell suite
bin/test ./internal/tui           # one Go package
bin/test --no-go 2>&1 | grep -E 'FAIL|TEST SUMMARY'
bin/test --timings                # after the run: slowest suites, Go packages and Go tests, total time
```

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
internal/tui/        Bubble Tea interface (-i)
adapters/bitbucket   bash adapter: the only code that knows curl and Bitbucket
bin/                 launcher, build/test/lint/setup/install/release scripts, bin/help
test/                *.test.sh suites; test/lib/bash_test.sh is the shared harness
.github/             workflows (ci, release) and dependabot.yml
```

## Test conventions

- Hermetic: no network, no real forge, no `op`, no real `~/.config`. Point `HOME`/`XDG_CONFIG_HOME` at a temp dir.
- Shell suites source `test/lib/bash_test.sh` (`test_pass`, `assert_eq`, `assert_contains`, `assert_status`, `run_tests`). Put stubs for `curl`, `gh`, `git` etc. in a temp dir prepended to `PATH`. `make_tool_dir DIR [TOOL...]` builds a minimal `PATH` that reliably lacks go, shellcheck, etc. Adapter-facing Go tests use fake adapter scripts.
- Go code takes injected seams (TTY check, editor runner, env lookup, stdin/stdout) instead of touching the real terminal or environment.
- Make waits configurable env vars and set them to `0` in tests (for example `RELEASE_RUN_WAIT_SECONDS`).
- Budget about 1-2 s per suite. Every assertion prints a `[PASS]`; a silent conditional is not a test.
- `internal/bbfake` is a fake Bitbucket Server (httptest) with scenario data, request recording and failure injection; it is test support only. `internal/bbfake/adapter_char_test.go` runs the real `adapters/bitbucket` against it for every subcommand and pins exit code, stdout, stderr and the requests sent in `internal/bbfake/testdata/adapter/*.golden.json` (refresh: `go test ./internal/bbfake -run Char -update`); it skips when bash, curl or jq is missing. A native adapter must pass the same cases.
- Help and docs must not drift: `internal/app/docs_test.go` fails when the README and `bbapprove help` disagree about flags or environment variables. Help texts are golden-tested; refresh with `go test ./internal/app -run Golden -update`.
- Real-terminal checks (TUI, prompts): run in a tmux session and send keys ONE AT A TIME with a short sleep between them; batched keys race the renderer. Wait for a marker in `tmux capture-pane` output before asserting.

## Adding a `bin/` script

1. `#!/usr/bin/env bash`, `# shellcheck shell=bash`, then line 3 exactly `# <name> — <description>`. `bin/help` scrapes that line.
2. `usage()` for `-h`/`--help` (exit 0); unknown option exits 2. Exit codes: 0 ok, 1 failure, 2 usage error, 127 missing tool.
3. `chmod +x`, and add `test/<name>.test.sh` (copy the shape of `test/release-notes.test.sh`).
4. Run `bin/lint bin/<name> test/<name>.test.sh`, `bin/test test/<name>.test.sh` and `bin/test test/help.test.sh`, then add a CHANGELOG line.
