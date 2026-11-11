#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
LC="$PROJECT_ROOT/bin/lint-commits"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
REPO=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
# Tests run in parallel subshells; each removes its own temp dir on exit.
bash_test_case_cleanup() { cleanup_test_tmp; }
trap 'cleanup_test_tmp' EXIT

OUT=""
RC=0

# g ARGS...: git in the fixture repo with a fixed identity and fixed dates.
g() {
  GIT_AUTHOR_DATE="2024-01-01T00:00:00Z" GIT_COMMITTER_DATE="2024-01-01T00:00:00Z" \
    git -C "$REPO" -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false -c maintenance.auto=false -c gc.auto=0 "$@"
}

# setup_repo: a repo whose main has one clean base commit; HEAD is on a feature branch.
setup_repo() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP/repo"
  git init -q -b main "$REPO"
  g commit -q --allow-empty -m "chore: base commit"
  g checkout -q -b feature
}

# commit SUBJECT [BODY]: add an empty commit with that message.
commit() {
  if [[ $# -ge 2 ]]; then
    g commit -q --allow-empty -m "$1" -m "$2"
  else
    g commit -q --allow-empty -m "$1"
  fi
}

# run ARGS...: run the linter inside the repo; sets OUT (stdout+stderr) and RC.
run() {
  RC=0
  OUT="$(cd "$REPO" && bash "$LC" "$@" 2>&1)" || RC=$?
}

# body_run TEXT: check TEXT as a body on stdin; sets OUT and RC.
body_run() {
  RC=0
  OUT="$(printf '%s\n' "$1" | bash "$LC" --body-file - 2>&1)" || RC=$?
}

# expect_clean LABEL: the feature branch passes.
expect_clean() {
  run main..HEAD
  assert_status 0 "$RC" "$1 passes"
  assert_eq "" "$OUT" "$1 prints nothing"
}

# expect_violation LABEL NEEDLE: the feature branch fails and the output names NEEDLE.
expect_violation() {
  run main..HEAD
  assert_status 1 "$RC" "$1 exits 1"
  assert_contains "$OUT" "$2" "$1 is reported"
}

# dflt: run the linter with no arguments inside the repo.
dflt() {
  RC=0
  OUT="$(cd "$REPO" && bash "$LC" 2>&1)" || RC=$?
}

test_range_selection() {
  setup_repo
  commit "feat(scope): add a thing" $'Add a thing because users asked for it.\n\nNot checked: Windows.'
  commit "fix!: break the api"
  expect_clean "a clean range"
  run HEAD..HEAD
  assert_status 0 "$RC" "an empty range exits 0"
  assert_eq "" "$OUT" "an empty range prints nothing"
  dflt
  assert_status 0 "$RC" "the default range works without origin/main (main..HEAD)"
  commit "oops bad subject"
  dflt
  assert_status 1 "$RC" "the default range finds the bad commit via main..HEAD"
  g update-ref refs/remotes/origin/main "$(g rev-parse HEAD)"
  dflt
  assert_status 0 "$RC" "origin/main..HEAD is preferred when origin/main exists"
  : >"$TEST_TMP/empty.md"
  run --title "feat: ok" --body-file "$TEST_TMP/empty.md" main..HEAD
  assert_status 1 "$RC" "an explicit range is checked next to --title and --body-file"
  cleanup_test_tmp
}

# title_run TEXT: check TEXT as a title; sets OUT and RC.
title_run() {
  RC=0
  OUT="$(bash "$LC" --title "$1" 2>&1)" || RC=$?
}

test_subject_length_boundary() {
  local s70 s71
  s70="fix: $(printf 'a%.0s' $(seq 1 65))"
  s71="${s70}a"
  assert_eq 70 "${#s70}" "fixture subject is 70 characters"
  title_run "$s70"
  assert_status 0 "$RC" "a 70 character subject passes"
  title_run "$s71"
  assert_status 1 "$RC" "a 71 character subject exits 1"
  assert_contains "$OUT" "title: subject is 71 characters, the limit is 70: $s71" "the length rule and the subject are printed"
}

test_bad_type_and_format() {
  local bad
  for bad in "feature: not a type" "Add a thing" "fix:no space" "fix(): x" "fix: "; do
    title_run "$bad"
    assert_status 1 "$RC" "'$bad' exits 1"
    assert_contains "$OUT" "subject is not 'type(scope): description'" "'$bad' is reported"
  done
  for bad in "feat: a" "fix(scope): a" "revert!: a" "perf(x)!: a" "ci: a" "build: a" "style: a"; do
    title_run "$bad"
    assert_status 0 "$RC" "'$bad' passes"
  done
}

test_trailing_period() {
  title_run "fix: something."
  assert_status 1 "$RC" "a trailing period exits 1"
  assert_contains "$OUT" "subject ends with a period" "a trailing period is reported"
}

test_label_headings() {
  local variant
  for variant in "What" "why:" "## Summary" "### Testing" "**Verification**" "**Notes:**" "OVERVIEW" "Changes" "Details" "Background" "Context" "Description"; do
    body_run "$(printf 'A sentence.\n\n%s\nmore text' "$variant")"
    assert_status 1 "$RC" "heading '$variant' exits 1"
    assert_contains "$OUT" "label heading" "heading '$variant' is reported"
    assert_contains "$OUT" "$variant" "heading '$variant' is quoted"
  done
}

test_inline_label() {
  body_run '**Why**: because.'
  assert_status 1 "$RC" "an inline bold label exits 1"
  assert_contains "$OUT" "inline label at line start" "an inline bold label is reported"
}

test_allows_descriptive_headings_and_prose_words() {
  body_run $'Why this matters is explained below.\n\n## Slow tests and their causes\n\nThe context is clear and the notes follow.'
  assert_status 0 "$RC" "descriptive headings and prose with label words pass"
}

test_this_pr() {
  body_run "This PR adds things."
  assert_status 1 "$RC" "'This PR' exits 1"
  assert_contains "$OUT" "says 'this PR'" "'This PR' is reported"
  body_run "Part of this pull request."
  assert_status 1 "$RC" "'this pull request' exits 1"
  body_run "Nothing about this prefix or this pragma."
  assert_status 0 "$RC" "words that merely start like PR pass"
}

test_file_list() {
  body_run $'Reason.\n\n- `a/b.go`\n- `c.sh`\n- `d/e.md`'
  assert_status 1 "$RC" "three bare file bullets exit 1"
  assert_contains "$OUT" "list of file paths" "a file list is reported"
  body_run $'Reason.\n\n- `a/b.go`\n- `c.sh`\n- keep this one\n- `d/e.md`\n- `f/g.md`'
  assert_status 0 "$RC" "two file bullets, a break, two more pass"
  body_run $'Reason.\n\n- `flag`\n- `--other`\n- `third`'
  assert_status 0 "$RC" "bullets of backticked words that are not paths pass"
}

test_each_commit_is_named_by_short_hash() {
  setup_repo
  commit "feat: ok"
  commit "bad subject"
  local short
  short="$(g rev-parse --short HEAD)"
  run main..HEAD
  assert_status 1 "$RC" "the bad commit fails the range"
  assert_contains "$OUT" "$short: subject is not" "violation line starts with the short hash"
  assert_contains "$OUT" "1 violation(s)" "a count closes the output"
  cleanup_test_tmp
}

test_merge_commits_are_skipped() {
  setup_repo
  commit "feat: on feature"
  g checkout -q main
  commit "fix: on main"
  g merge -q --no-ff feature -m "Merge branch feature (not conventional)"
  g checkout -q -b after
  g update-ref refs/heads/base "$(g rev-parse HEAD~1)"
  run base..HEAD
  assert_status 0 "$RC" "a non-conventional merge commit is skipped"
  cleanup_test_tmp
}

test_title_mode() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP"
  run --title "feat(x): fine"
  assert_status 0 "$RC" "a good --title passes outside a git repo"
  run --title "Fix things."
  assert_status 1 "$RC" "a bad --title exits 1"
  assert_contains "$OUT" "title: subject is not" "the title is named 'title'"
  assert_contains "$OUT" "title: subject ends with a period" "title gets the period rule"
  run --title "fix: $(printf 'a%.0s' $(seq 1 66))"
  assert_contains "$OUT" "subject is 71 characters" "title gets the length rule"
  cleanup_test_tmp
}

test_body_file_mode() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP"
  printf 'Fix the thing because it broke.\n\nNot checked: Windows.\n' >"$TEST_TMP/ok.md"
  printf '## Summary\n\nThis PR does it.\n' >"$TEST_TMP/bad.md"
  run --body-file "$TEST_TMP/ok.md"
  assert_status 0 "$RC" "a clean body file passes"
  run --body-file "$TEST_TMP/bad.md"
  assert_status 1 "$RC" "a bad body file exits 1"
  assert_contains "$OUT" "body: label heading" "the heading is reported as 'body'"
  assert_contains "$OUT" "body: says 'this PR'" "'this PR' is reported as 'body'"
  RC=0
  OUT="$(cd "$REPO" && bash "$LC" --body-file - <"$TEST_TMP/bad.md" 2>&1)" || RC=$?
  assert_status 1 "$RC" "--body-file - reads stdin"
  assert_contains "$OUT" "body: label heading" "stdin body is checked"
  cleanup_test_tmp
}

test_usage_errors_exit_2() {
  setup_repo
  run --bogus
  assert_status 2 "$RC" "unknown option exits 2"
  assert_contains "$OUT" "unknown option: --bogus" "unknown option is named"
  run --title
  assert_status 2 "$RC" "--title without a value exits 2"
  run --body-file "$TEST_TMP/missing.md"
  assert_status 2 "$RC" "a missing body file exits 2"
  run a..b c..d
  assert_status 2 "$RC" "two ranges exit 2"
  run nosuchref..HEAD
  assert_status 2 "$RC" "an unresolvable range exits 2"
  run --help
  assert_status 0 "$RC" "--help exits 0"
  assert_contains "$OUT" "Usage: bin/lint-commits" "--help prints usage"
  cleanup_test_tmp
}

# Tests share no state (each has its own temp dir), so they run in parallel; BASH_TEST_JOBS=1 runs them serially.
BASH_TEST_JOBS="${BASH_TEST_JOBS:-4}"
run_tests "$@"
