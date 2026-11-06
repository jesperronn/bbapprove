# TODO

- [ ] **Interactive interface** (fzf or similar): pick PRs from the eligible list instead of all-or-nothing `--apply`.
- [ ] **TUI dashboard**: checklist of PRs, approve/merge one by one.
  - Colorize: dividers per project/repo, build status, SonarQube/quality-gate status, skipped-vs-eligible, merge-check failures.
  - Pull build/quality status from the PR (build-status and merge-check endpoints).
- [ ] **`--help`**: list every flag with a short, concise explanation (current help is partial: add `--apply`, `--disallow`, `--token`, env vars, and any new flags).
- [ ] **Wrapper in `dotfiles/bin`** that calls `~/src/bbapprove/bin/bbapprove` (with `bin/<name>.test.sh`, per dotfiles rules).
  - NOTE: the request was cut off after "a wrapper that calls". Assumed it calls bbapprove; confirm name and intent.
