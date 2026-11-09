# AGENTS.md — bbapprove

Approve and merge Renovate PRs from the terminal (Go core, bash launcher and forge adapter). Instructions for AI agents and new maintainers.

## Commands

```
bin/help      list every command          bin/lint      gofmt, go vet, shellcheck (--autofix, PATH...)
bin/setup     check tools, fetch, build   bin/test      Go + shell suites (--verbose --no-go --no-shell TARGET)
bin/build     build bin/bbapprove-go      bin/install   build + link the launcher into ~/.local/bin
bin/release X.Y.Z   changelog, tag, push, watch the workflow (--dry-run first)
```

## Definition of done

Run `bin/lint`, then `bin/test`. Both must be green, and you must have seen the output, before you say "done".

## Conventions

- Conventional commits: `feat|fix|chore|docs|test|refactor(scope): subject`.
- One logical change per commit. Stage only your files by path; never `git add -A`.
- A new or changed script in `bin/` gets a `test/<name>.test.sh`.
- Add a line under `## [Unreleased]` in `CHANGELOG.md` for user-visible changes.
- On macOS use `gsed` (GNU sed), not BSD `sed`.

## Hard rules

- Never commit `bbapprove.config.toml` (git-ignored) or anything specific to an employer or person: hosts, emails, token item names, project or repo names. Use `example.com`, `PROJ/repo`, `renovate-bot@example.com`.
- `internal/config/bbapprove.config.template` must stay byte-identical to `bbapprove.config.template`: after editing the root file run `cp bbapprove.config.template internal/config/`.
- Tests are hermetic: no network, no real forge, no `op`, no real `~/.config`. Stub tools on `PATH`.
- The adapter (`adapters/<type>`) is the only code that talks to the forge.
- Never push, tag or release unless asked; `bin/release` does it.

## More

[docs/README.md](docs/README.md) indexes: [development](docs/development.md), [architecture](docs/architecture.md), [releasing](docs/releasing.md), [CI](docs/ci.md). User docs: [README.md](README.md); open work: [TODO.md](TODO.md).
