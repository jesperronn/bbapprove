# bbapprove

Approve and merge Renovate PRs on Bitbucket Server (bitbucket.example.com) via the REST API.
Dry-run by default; `--apply` approves, then merges.

```
bin/bbapprove                      # report eligible PRs where I'm reviewer
bin/bbapprove --apply              # approve + merge them
bin/bbapprove --apply <PR_URL>     # just one PR
```

Eligible: author `renovate-bot@example.com`, subject starts `chore(deps):` / `fix(deps):`,
not on the disallow list (`disallow.txt` next to `bin/`: one dependency per line, `#` comments and blank lines ignored, currently `some-library` and `another-library`; `--disallow a,b` adds more), minor/patch/digest only
(title `to vN` or branch `renovate/major-*` counts as major).

Network (in the Bitbucket adapter): every curl call has `--connect-timeout 2.7 --max-time 3.5`; PR fetches and `--apply` run in parallel via `xargs -P`, one job per CPU core (`BBAPPROVE_JOBS`).

Token: `BBAPPROVE_TOKEN`, `--token`, or 1Password `BITBUCKET_TOKEN`.

Architecture: `bin/bbapprove` holds eligibility rules, the disallow list and the UI; it never talks to the forge.
All forge access goes through an adapter, a `gh`-style CLI (`BBAPPROVE_ADAPTER`, default `adapters/bitbucket`):

```
adapter auth token
adapter pr list --reviewer @me --state open   # JSON array of normalized PRs
adapter pr view URL                           # one normalized PR
adapter pr review URL --approve
adapter pr merge URL                          # retries stale versions itself
adapter pr comment URL --body TEXT            # or --body-file FILE; empty body exits 2
adapter pr close URL [--comment TEXT]         # decline; --comment is posted first, and a failed comment aborts the decline
```

Normalized PR: `{url, number, project, repo, title, author:{email,login}, headBranch, approvedByMe}`.
A GitHub/GitLab adapter only needs to implement these commands.

Test: `test/bbapprove.test.sh`, `test/bitbucket-adapter.test.sh`. Lint: `shellcheck bin/bbapprove adapters/* test/*.sh`.
