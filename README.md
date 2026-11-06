# bbapprove

Approve and merge Renovate PRs on Bitbucket Server (bitbucket.example.com) via the REST API.
Dry-run by default; `--apply` approves, then merges.

```
bin/bbapprove                      # report eligible PRs where I'm reviewer
bin/bbapprove --apply              # approve + merge them
bin/bbapprove --apply <PR_URL>     # just one PR
```

Eligible: author `renovate-bot@example.com`, subject starts `chore(deps):` / `fix(deps):`,
not on the disallow list (`--disallow a,b`, default `some-library`), minor/patch/digest only
(title `to vN` or branch `renovate/major-*` counts as major).

Token: `BBAPPROVE_TOKEN`, `--token`, or 1Password `BITBUCKET_TOKEN`.

Test: `test/bbapprove.test.sh`. Lint: `shellcheck bin/bbapprove test/*.sh`.
