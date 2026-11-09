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

## Commit messages and PR descriptions

- The subject is conventional (`type(scope): description`), imperative and at most 70 characters: `fix(lint): run tr under LC_ALL=C`. The body may be omitted when the subject already gives the change and the reason (a typo fix, a rename).
- Keep it short: a commit body is at most about 80 words, a PR description at most about 200. Only a PR that carries a list of decisions may run longer, and then as bullets. The first sentence gives the change and the reason together; every later sentence must tell a reviewer something the diff, the title and the tests do not (a measurement, a trade-off, a decision, a known gap). If it does not, delete it.
- Leave out what the diff shows: file, test and case names, which docs or help screens were updated, formatting side effects (a column width, regenerated output), counts of added tests beyond one clause. Mention regenerated expected output only when a reviewer would otherwise be surprised by a large diff.
- Use plain words. Project jargon (`golden`, `characterization suite`, `fake`) means nothing to a reader without the repo in their head: say `recorded expected output`, `tests against a stand-in server`.
- No label headings. `What`, `Why`, `Summary`, `Overview`, `Changes`, `Details`, `Testing`, `Verification` and `Notes` carry no information; write the sentence instead. A heading is allowed only when its text names its content (`Slow tests and their causes`, `Quirks pinned, not fixed`), and a short PR needs none.
- Say what was checked as one sentence with the real commands (`go test -race, bin/lint and bin/test pass`), and what was not checked as one plain sentence naming only what matters (`Not checked: the delete call against a real Bitbucket`). Never write a claim you did not see output for.
- No "this PR", no restating the title. Stacked PRs say which PR must merge first.
- Use bullets only for parallel items; use a table only for data (timings, option lists).
- Run `bin/lint-commits` before pushing. It checks the mechanical rules only; the rest (length, imperative mood, plain words, change plus reason, no unverified claims) is on the author.

## Hard rules

- Never commit `bbapprove.config.toml` (git-ignored) or anything specific to an employer or person: hosts, emails, token item names, project or repo names. Use `example.com`, `PROJ/repo`, `renovate-bot@example.com`.
- `internal/config/bbapprove.config.template` must stay byte-identical to `bbapprove.config.template`: after editing the root file run `cp bbapprove.config.template internal/config/`.
- Tests are hermetic: no network, no real forge, no `op`, no real `~/.config`. Stub tools on `PATH`.
- The adapter (`adapters/<type>`) is the only code that talks to the forge.
- Never push, tag or release unless asked; `bin/release` does it.

## Docs

One source per fact: flags, env vars and exit codes live in `bbapprove --help` and its topics (`internal/app/help.go`), the README only links to them, and `internal/app/docs_test.go` enforces it. Design rationale lives only in [docs/architecture.md](docs/architecture.md). Maintainer docs: [docs/README.md](docs/README.md). Open work: [TODO.md](TODO.md).
