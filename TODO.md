# TODO

- [x] **Interactive interface** (`-i/--interactive`): Bubble Tea TUI listing the PRs with mark, approve+merge, decline, comment, ignore and open.
- [ ] **TUI dashboard polish**
  - [x] Approve/merge one by one or in bulk, with live result per row.
  - [x] Build status, SonarQube/quality-gate status and approval/comment/task columns (fed from the PR).
  - [ ] Dividers per project/repo.
  - [ ] Show merge-check failures before acting (today a failed merge only shows after the attempt).
  - [ ] Build and SonarQube links (`o` currently opens only the PR).
- [x] **`--help`**: lists every flag, the eligibility rules and the env vars.
- [x] **Wrapper in `dotfiles/bin`** (`bin/bbapprove`): injects the 1Password token and passes all flags through. The repo's `bin/bbapprove` is now the Go launcher at the same path.
- [ ] **Major-version detection must not rely on branch names or title heuristics.** Never auto-approve a major bump.
  - Today: title ending `to vN` or branch `renovate/major-*` counts as major (`internal/rules`). Drop the branch check, and treat "can't tell" as NOT eligible (fail closed).
  - Compare the real old vs new version instead (e.g. from the PR diff or the Renovate PR description table), and only approve minor/patch/digest.
  - Check the vitest and spotless bumps that were reported eligible in the first dry run.
- [ ] **CI, release and dependency updates** (decide the host first: `origin` has no URL yet).
  - CI on every push/PR: `bin/lint`, then `bin/runtests` (Go tests with `-race` plus the shell suites), pinned Go version from `go.mod`.
  - Release on tag `v*`: `bin/build` with the git-derived version, cross-compiled binaries (darwin arm64/amd64, linux amd64), checksums, notes from the matching `CHANGELOG.md` section. Pre-release for `-rc` tags.
  - Dependency updates for `go.mod` and the CI config: Dependabot only exists on GitHub; on Bitbucket/Jenkins use Renovate (what the team already uses) with `gomodTidy` enabled. Start from the repo's own `bbapprove` rules: minor/patch/digest auto-merge, majors reviewed by a human.
