# AGENTS.md — bbapprove

Approve and merge Renovate PRs from the terminal (Go core, bash launcher and forge adapter). Instructions for AI agents and new maintainers.

## Definition of done

`bin/help` lists the commands. Run `bin/lint`, then `bin/test`. Both must be green, and you must have seen the output, before you say "done".

## Conventions

- Conventional commits: `feat|fix|chore|docs|test|refactor(scope): subject`.
- One logical change per commit. Stage only your files by path; never `git add -A`.
- A new or changed script in `bin/` gets a `test/<name>.test.sh`.
- Add a line under `## [Unreleased]` in `CHANGELOG.md` for user-visible changes (changes only, no how-to).
- On macOS use `gsed` (GNU sed), not BSD `sed`.

## Hard rules

- Never commit `bbapprove.config.toml` (git-ignored) or anything specific to an employer or person: hosts, emails, token item names, project or repo names. Use `example.com`, `PROJ/repo`, `renovate-bot@example.com`.
- `internal/config/bbapprove.config.template` must stay byte-identical to `bbapprove.config.template`: after editing the root file run `cp bbapprove.config.template internal/config/`.
- Tests are hermetic: no network, no real forge, no `op`, no real `~/.config`. Stub tools on `PATH`.
- The adapter (`adapters/<type>`) is the only code that talks to the forge.
- Never push, tag or release unless asked; `bin/release` does it.

## Docs

One source per fact: flags, env vars and exit codes live in `bbapprove --help` and its topics (`internal/app/help.go`), the README only links to them, and `internal/app/docs_test.go` enforces it. Design rationale lives only in [docs/architecture.md](docs/architecture.md). Maintainer docs: [docs/README.md](docs/README.md). Open work: [TODO.md](TODO.md).
