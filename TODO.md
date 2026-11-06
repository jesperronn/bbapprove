# TODO

- [x] **Interactive interface** (`-i/--interactive`): fzf multi-select of eligible PRs, sorted by repo, all preselected; ENTER approves+merges the picks.
- [ ] **TUI dashboard**: checklist of PRs, approve/merge one by one.
  - Colorize: dividers per project/repo, build status, SonarQube/quality-gate status, skipped-vs-eligible, merge-check failures.
  - Pull build/quality status from the PR (build-status and merge-check endpoints).
- [ ] **`--help`**: list every flag with a short, concise explanation (current help is partial: add `--apply`, `--disallow`, `--token`, env vars, and any new flags).
- [x] **Wrapper in `dotfiles/bin`** (`bin/bbapprove`): injects the 1Password token and passes all flags through.
- [ ] **Major-version detection must not rely on branch names or title heuristics.** Never auto-approve a major bump.
  - Today: title ending `to vN` or branch `renovate/major-*` counts as major. Drop the branch check, and treat "can't tell" as NOT eligible (fail closed).
  - Compare the real old vs new version instead (e.g. from the PR diff or the Renovate PR description table), and only approve minor/patch/digest.
  - Check the vitest and spotless bumps that were reported eligible in the first dry run.
