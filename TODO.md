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
  - [x] Release run verified end to end: the Release workflow succeeded for v1.0.0-rc.2, v1.0.0, v1.1.0 and v1.2.0, and a Windows zip is built (`bin/package`).
  - [ ] Build provenance attestations (`actions/attest-build-provenance`): needs a plan that supports attestations for private repos. Then verify release assets with `gh attestation verify` in addition to SHA256SUMS.
- [ ] **Search / filter in the TUI (1.1)**: `/` filters the list as you type (plain case-insensitive substring terms,
  AND-ed; `!term` excludes) over status, `PROJECT/repo#id`, title and skip reason. Enter keeps the filter, Esc clears it,
  footer shows `filter: <text> (shown/total)`. `ctrl+a` marks eligible visible rows; `A`/`D`/`C` act only on marked rows
  that are visible (hidden marks stay, footer shows how many are hidden). Later: fuzzy matching and field prefixes
  (`repo:`, `status:`, `build:failed`).
- [x] **Standard scripts**: `bin/setup`, `bin/test`, `bin/lint`, `bin/build`, `bin/install`, `bin/release`, `bin/help`.
- [x] **Maintainer docs**: `AGENTS.md` and `docs/` (development, architecture, releasing, CI).
- [x] **First-run helper**: create the config from the embedded template (`--init`, also offered interactively on a first run).
- [ ] **Quality gate in the single-PR view**: `ViewPR` in `internal/forge/bitbucket/commands.go` always reports the quality gate as "unknown" because the single-PR GET has no `qgStatus` (the list call does). Check against a real server whether another endpoint exposes it, then fix the code and drop the stale comment.
