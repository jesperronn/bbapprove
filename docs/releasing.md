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

## What the Release workflow builds

On a pushed `v*` tag (`.github/workflows/release.yml`): it reruns CI, then cross-builds with `bin/build` for darwin and linux on arm64 and amd64 and uploads four `bbapprove-<tag>-<os>-<arch>.tar.gz` archives plus `SHA256SUMS`. Each archive holds `bin/bbapprove`, `bin/bbapprove-go`, `adapters/bitbucket`, `bbapprove.config.template`, `README.md`, `CHANGELOG.md`, `LICENSE`.
The release notes are the matching CHANGELOG section, printed by `bin/release-notes vX.Y.Z` (exit 1 if the section is missing or empty). A tag containing `-` is marked pre-release.

## How users update

Users update with `bbapprove --update`, described in [README](../README.md#updating). It consumes the release assets
named `bbapprove-<tag>-<os>-<arch>.tar.gz` plus `SHA256SUMS`, so renaming those assets in `release.yml` breaks it.

## Versioning

Semver. The version comes from git: `git describe --tags --always --dirty` (for example `v1.0.0-rc.2`, `v1.0.0-rc.2-3-gabc1234`), injected with `-ldflags "-X main.version=..."` by `bin/build`; `bin/build --version` prints it without building. Without git it is `dev`. `BBAPPROVE_VERSION` overrides it (the release workflow sets it to the tag). `bbapprove --version` shows it.

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
