# CI

What the GitHub Actions workflows and Dependabot do, and how to reproduce a runner failure locally. Release specifics: [releasing.md](releasing.md).

## Workflows (`.github/`)

- `workflows/ci.yml` (CI): on push to `main`, pull requests, manual runs and as a reusable workflow. Matrix `ubuntu-latest` and `macos-latest`, `fail-fast: false`, 20 minute timeout. Steps: checkout (full history, for `git describe`), Go from `go.mod`, install tools, print tool versions, `bin/lint`, `bin/test`. Same-ref runs are cancelled on pull requests.
- Job `go-windows` in the same file: `windows-latest`, 20 minute timeout, runs `go build`, `go vet` and `go test` on all packages. It blocks merging like the `check` job.
- `workflows/release.yml` (Release): on a pushed `v*` tag. Job `checks` calls `ci.yml`; job `release` (needs `checks`, `contents: write`) builds five archives (four `tar.gz` and a Windows zip), `SHA256SUMS`, notes via `bin/release-notes`, and `gh release create --verify-tag` (adds `--prerelease` when the tag has a `-`).
- `dependabot.yml`: weekly on Monday for `gomod` and `github-actions`, minor and patch updates grouped, commit prefix `chore(deps)` and label `dependencies` (so bbapprove's own eligibility rule recognises them), at most 5 open PRs. Majors arrive as separate PRs for a human.

## Runner tools

Both OSes need bash 4+ (`mapfile`), ripgrep (`rg`, used by `test/lib/bash_test.sh`), shellcheck and shfmt (`bin/lint`, which skips shfmt when it is missing, so CI installs it to keep formatting enforced) and jq. Linux installs them with apt. macOS installs `bash ripgrep shellcheck shfmt jq` with Homebrew and puts the Homebrew `bin` first on `PATH`, because `/usr/bin/env bash` would otherwise find macOS bash 3.2. `bin/setup --check` verifies the same list locally (shfmt is reported as optional there).

## SHA pinning

Actions are pinned by full commit SHA with the tag in a trailing comment (`uses: actions/checkout@<sha> # v7.0.1`). Dependabot bumps the SHA and the comment together; review those PRs like any other. Never replace a SHA with a moving tag.

## Reproduce a Linux failure with podman

```
podman run --rm -it -v "$PWD":/src:Z -w /src docker.io/library/golang:1.27 bash -c '
  apt-get update && apt-get install -y --no-install-recommends ripgrep shellcheck shfmt jq git &&
  git config --global --add safe.directory /src && bin/lint && bin/test'
```

Keep the image tag in line with the `go` line in `go.mod`. Build output in `bin/bbapprove-go` is a Linux binary afterwards; delete it before going back to macOS (`rm bin/bbapprove-go`).

## Flaky-test lesson

The Bubble Tea renderer redraws only changed cells at a capped frame rate, so under load typed text such as `Hello!` arrives in pieces and is never a contiguous substring of the output. A real-program TUI test (`internal/tui/shift_test.go`, fixed in `dfee73c`) failed on Linux for that reason. Wait for markers that appear only once earlier input is processed (a prompt string, the last typed character followed by the cursor), use a generous deadline, dump the output on timeout, and assert exact text on the model instead. See [development.md](development.md#test-conventions).
