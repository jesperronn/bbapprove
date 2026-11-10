# Releasing

How to cut a release with `bin/release`, what the Release workflow publishes, and how the changelog and versions work.

## Flow

1. While working, add entries under `## [Unreleased]` in `CHANGELOG.md` (Keep a Changelog headings `### Added`, `### Changed`, `### Fixed`).
2. Preview: `bin/release --dry-run 1.0.0` (read-only checks; prints every step it would take).
3. Release: `bin/release 1.0.0` (or `1.0.0-rc.3`; no leading `v`; a `-` makes it a pre-release).

Preconditions (checked first, nothing is changed if one fails): on `main`, clean tree, not behind `origin/main`, tag `vX.Y.Z` unused locally and on origin, `gh auth status` ok, `[Unreleased]` not empty.
Local commits ahead of origin are allowed; they are pushed too.

Steps: `bin/lint`, `bin/test`, move `[Unreleased]` under `## [X.Y.Z] - DATE` (leaving an empty `[Unreleased]`), commit `docs: changelog for X.Y.Z`, annotated tag `vX.Y.Z`, ask `Push main and vX.Y.Z to origin? [y/N]` (skip with `--yes`), `git push --atomic origin main vX.Y.Z`, `gh run watch` the Release workflow, print the release URL.

Answered no? Nothing was pushed; the changelog commit and the tag exist locally. Push later with `git push --atomic origin main vX.Y.Z`, or undo with `git tag -d vX.Y.Z && git reset --hard HEAD~1`.
Exit codes: 0 released, 1 failed or declined, 2 usage error, 127 tool missing. `RELEASE_DATE` overrides the date.

## Changelog generator

`bin/changelog` derives the next version and a CHANGELOG section from the conventional-commit subjects since the last `v*` tag. It is a maintainer tool (`cmd/changelog`, library `internal/release`); the release archives do not contain it, and `bin/release` does not use it yet.

```
bin/changelog next                    # prints vX.Y.Z; exit 0 releasable, 3 nothing releasable
bin/changelog render                  # prints the "## [X.Y.Z] - DATE" section
bin/changelog update                  # inserts it above the newest release in CHANGELOG.md
bin/changelog due                     # prints the time left of the quiet period; exit 0 due, 4 not yet
```

Flags: `--repo DIR`, `--version X`, `--date YYYY-MM-DD`, `--file`, `--replace-unreleased` (drops the hand-written `[Unreleased]` block; without it that block stays and the new section goes below it), `--grace 2h`, `--last-activity` and `--now` (RFC3339; `--last-activity` defaults to the HEAD commit time). `bin/changelog SUBCOMMAND -h` lists them. `update` refuses when the version's heading already exists.

The subject line is the changelog entry, so write subjects for the people who read the changelog. The entry is `- **scope:** description` as written (the hash is left out); a commit body is never copied.

| Commit | Bump | Listed under |
| --- | --- | --- |
| `!` before the colon, or a `BREAKING CHANGE:` / `BREAKING-CHANGE:` footer line | major | `### Changed (BREAKING)`, first |
| `feat` | minor | `### Added` |
| `fix` | patch | `### Fixed` |
| `perf` | patch | `### Changed` |
| `revert` | none by itself | `### Changed` |
| `docs`, `test`, `chore`, `refactor`, `ci`, `build`, `style` | none | not listed |
| anything that is not a conventional subject (merge commits, stray text) | none | ignored |

The largest bump wins. Only non-listed commits since the tag means nothing is releasable. Bumps are plain semver, also below 1.0 (a breaking change on 0.3.1 gives 1.0.0). When the last tag is a pre-release such as `v1.0.0-rc.2`, the next version is its stable core `1.0.0`, whatever the bump. With no tag the base is `0.0.0`. The table of types lives in `internal/release/render.go` (`SectionOf`); the quiet period is `release.DefaultGrace` (2 hours) in `internal/release/git.go`.

## What the Release workflow builds

On a pushed `v*` tag (`.github/workflows/release.yml`): it reruns CI, then runs `bin/package TAG --out dist` and uploads five archives plus `SHA256SUMS`. `bin/package` cross-builds with `bin/build`; run it locally to check an archive (`--only OS/ARCH`, repeatable, narrows the targets; nothing is published).
Four are `bbapprove-<tag>-<os>-<arch>.tar.gz` for darwin and linux on arm64 and amd64; each holds `bin/bbapprove`, `bin/bbapprove-go`, `adapters/bitbucket`, `bbapprove.config.template`, `README.md`, `CHANGELOG.md`, `LICENSE`.
The fifth is `bbapprove-<tag>-windows-amd64.zip` (amd64 only; Windows on ARM runs it through emulation). It holds one folder, `bbapprove-<tag>-windows-amd64/`, with `bbapprove.exe`, `bbapprove.config.template`, `README.md`, `CHANGELOG.md` and `LICENSE`: no launcher, no `bin/` and no `adapters/`, because the Bitbucket adapter is built into the exe. User config lives in `%AppData%\bbapprove`.
The release notes are the matching CHANGELOG section, printed by `bin/release-notes vX.Y.Z` (exit 1 if the section is missing or empty). A tag containing `-` is marked pre-release.

## How users update

Users update with `bbapprove --update` (`bbapprove help update`). It consumes the release assets
named `bbapprove-<tag>-<os>-<arch>.tar.gz` (`.zip` on Windows) plus `SHA256SUMS`, so renaming those assets in `release.yml` breaks it.

## Versioning

Semver. The version comes from git: `git describe --tags --always --dirty` (for example `v1.0.0-rc.2`, `v1.0.0-rc.2-3-gabc1234`), injected with `-ldflags "-X main.version=..."` by `bin/build`; `bin/build --version` prints it without building. Without git it is `dev`. `BBAPPROVE_VERSION` overrides it (the release workflow sets it to the tag). `bbapprove --version` prints it; it is also on the first line of `--help` and dim in the TUI footer.

## Post-release checklist

```
gh release download v1.0.0 --pattern '*darwin-arm64*' --pattern SHA256SUMS --dir /tmp/bbapprove-rel
cd /tmp/bbapprove-rel && shasum -a 256 -c SHA256SUMS --ignore-missing
tar -xzf bbapprove-v1.0.0-darwin-arm64.tar.gz && bbapprove-v1.0.0-darwin-arm64/bin/bbapprove --version
bbapprove-v1.0.0-darwin-arm64/bin/bbapprove --help | head -3
```

- The version must print the tag; the release page shows the notes and pre-release flag as expected.
- Archives downloaded in a browser get the macOS quarantine flag; clear it with `xattr -dr com.apple.quarantine <dir>`. `gh`/`curl` downloads are not flagged.
- Known gap: no build provenance or attestation yet (see [../TODO.md](../TODO.md)).

Workflow details and pinning: [ci.md](ci.md).
