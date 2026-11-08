# TODO

- [x] **Interactive interface** (`-i/--interactive`): Bubble Tea TUI listing the PRs with mark, approve+merge, decline, comment, ignore and open.
- [ ] **TUI dashboard polish**
  - [x] Approve/merge one by one or in bulk, with live result per row.
  - [x] Build status, SonarQube/quality-gate status and approval/comment/task columns (fed from the PR).
  - [ ] Dividers per project/repo.
  - [ ] Show merge-check failures before acting (today a failed merge only shows after the attempt).
  - [ ] Build and SonarQube links (`o` currently opens only the PR).
- [x] **`--help`**: lists every flag, the eligibility rules and the env vars.
- [x] **Configuration file**: site settings (forge URL, token item, bot author, disallow list) live in a git-ignored TOML file; see README.
- [ ] **Major-version detection must not rely on branch names or title heuristics.** Never auto-approve a major bump.
  - Today: title ending `to vN` or branch `renovate/major-*` counts as major (`internal/rules`). Drop the branch check, and treat "can't tell" as NOT eligible (fail closed).
  - Compare the real old vs new version instead (e.g. from the PR diff or the Renovate PR description table), and only approve minor/patch/digest.
  - Check the vitest and spotless bumps that were reported eligible in the first dry run.
- [x] **CI, release and dependency updates**: GitHub Actions CI, tag-driven release and Dependabot (`.github/`).
  - [ ] Build provenance attestations (`actions/attest-build-provenance`): needs a plan that supports attestations for private repos.
  - [ ] Verify the first release run end to end (`v*` tag on GitHub) and add a Windows build only if someone needs it.
