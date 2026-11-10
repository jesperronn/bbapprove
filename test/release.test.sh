#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

ORIG_PATH="$PATH"
REAL_GIT="$(command -v git)"
TEST_TMP=""
TPL=""
# bash_test_case_cleanup runs at the end of each test (they run in parallel subshells); only the suite removes TPL.
bash_test_case_cleanup() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
cleanup_test_tmp() {
  bash_test_case_cleanup
  [[ -n "$TPL" ]] && rm -rf "$TPL"
  return 0
}
trap 'cleanup_test_tmp' EXIT

CHANGELOG_BEFORE='# Changelog

## [Unreleased]

### Added
- New thing

## [0.9.0] - 2026-01-01

### Fixed
- Old thing'

CHANGELOG_AFTER='# Changelog

## [Unreleased]

## [1.0.0-rc.3] - 2026-02-03

### Added
- New thing

## [0.9.0] - 2026-01-01

### Fixed
- Old thing'

# Fixture: a work repo on main with a local bare "origin" (real git, nothing leaves the temp dir), the real
# bin/release and bin/release-notes, recording stubs for bin/lint and bin/test, a git wrapper that logs
# commit/tag/push and passes through, and a gh stub. Every mutating call lands in $CALLS ($STUB_LOG).
# The stubs are the fixed ones in test/fixtures, linked in (never written per test). The git repos are built
# once per suite (TPL) and copied per test; bin/release and bin/release-notes are hardlinked from the TPL copy
# so they stay warm instead of being fresh executables each test. No test writes to them.
STUBS=""
# build_template: the shared git repos and stubs, built once before any test runs (the tests run in parallel).
build_template() {
  TPL="$(mktemp -d)"
  STUBS="$TPL/stubs"
  use_stubs "$STUBS" git gh noop=sleep
  mkdir -p "$TPL/work/bin"
  cp "$PROJECT_ROOT/bin/release" "$PROJECT_ROOT/bin/release-notes" "$TPL/work/bin/"
  use_stubs "$TPL/work/bin" recorder=lint recorder=test printer=changelog
  printf '%s\n' "$CHANGELOG_BEFORE" >"$TPL/work/CHANGELOG.md"
  "$REAL_GIT" init -q --template= -b main "$TPL/work"
  "$REAL_GIT" init -q --bare --template= -b main "$TPL/origin.git"
  "$REAL_GIT" -C "$TPL/work" add -A
  "$REAL_GIT" -C "$TPL/work" commit -q -m initial
  "$REAL_GIT" -C "$TPL/work" remote add origin "$TPL/origin.git"
  "$REAL_GIT" -C "$TPL/work" push -q -u origin main
}

setup_fixture() {
  [[ -n "$TPL" ]] || build_template
  TEST_TMP="$(mktemp -d)"
  W="$TEST_TMP/work"
  O="$TEST_TMP/origin.git"
  CALLS="$TEST_TMP/calls.log"
  export STUB_LOG="$CALLS" STUB_GIT_REAL="$REAL_GIT" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
  export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.test GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.test
  export RELEASE_DATE=2026-02-03 BBAPPROVE_RELEASE_RUN_WAIT_SECONDS=0 BBAPPROVE_RELEASE_RUN_POLL_SECONDS=1
  unset STUB_LINT_RC STUB_TEST_RC STUB_GH_AUTH_RC STUB_GH_WATCH_RC STUB_GH_RUN_APPEARS_ON STUB_GH_RUN_LIST_ERR STUB_CHANGELOG_OUT STUB_CHANGELOG_RC
  cp -R "$TPL/work" "$W"
  cp -R "$TPL/origin.git" "$O"
  link_script "$TPL/work/bin/release" "$W/bin/release"
  link_script "$TPL/work/bin/release-notes" "$W/bin/release-notes"
  "$REAL_GIT" -C "$W" remote set-url origin "$O"
  : >"$CALLS"
}

# run_release ANSWER ARGS...: runs bin/release from the fixture, answer on stdin; sets OUT and RC.
run_release() {
  local answer="$1"
  shift
  RC=0
  OUT="$(cd "$W" && printf '%s\n' "$answer" | env PATH="$STUBS:$ORIG_PATH" "$W/bin/release" "$@" 2>&1)" || RC=$?
}

head_sha() { "$REAL_GIT" -C "$W" rev-parse HEAD; }

# calls_summary: the logged commands, one per line, reduced to their first two words.
calls_summary() { awk '{ print $1 " " $2 }' "$CALLS" | paste -sd'|' -; }

assert_nothing_changed() {
  local label="$1"
  assert_eq "" "$(grep -E '^git (commit|tag|push)' "$CALLS" || true)" "$label: no commit, tag or push"
  assert_eq "" "$("$REAL_GIT" -C "$W" tag)" "$label: no local tag"
  assert_eq "initial" "$("$REAL_GIT" -C "$W" log -1 --format=%s)" "$label: HEAD is still the initial commit"
}

test_bad_versions_exit_2() {
  setup_fixture
  local v
  for v in v1.0.0 1.0 1 01.0.0 1.0.0- 1.0.0-rc..1 abc; do
    run_release n "$v"
    assert_status 2 "$RC" "version '$v' is rejected with exit 2"
  done
  assert_contains "$OUT" "not a semver version" "message explains"
  assert_nothing_changed "bad version"
}

test_usage_errors_and_help() {
  setup_fixture
  run_release n
  assert_status 1 "$RC" "no VERSION and a failing bin/changelog exits 1"
  assert_contains "$OUT" "cannot derive the version" "and says so"
  run_release n --bogus 1.0.0
  assert_status 2 "$RC" "unknown flag exits 2"
  assert_contains "$OUT" "unknown option: --bogus" "unknown flag is named"
  run_release n --help
  assert_status 0 "$RC" "--help exits 0"
  assert_contains "$OUT" "Usage: bin/release" "--help prints usage"
}

test_omitted_version_is_derived_from_conventional_commits() {
  setup_fixture
  STUB_CHANGELOG_RC=0 STUB_CHANGELOG_OUT=v1.2.0 run_release "" --yes
  assert_status 0 "$RC" "release without VERSION succeeds"
  assert_contains "$OUT" "Release v1.2.0 (2026-02-03); version derived from conventional commits" "the header says it was derived"
  assert_contains "$OUT" "Released v1.2.0: https://example.test/releases/v1.2.0" "the derived version is released"
  assert_eq "v1.2.0" "$("$REAL_GIT" -C "$W" tag)" "the tag has the derived version"
  assert_eq "docs: changelog for 1.2.0" "$("$REAL_GIT" -C "$W" log -1 --format=%s)" "commit message has the derived version"
  assert_not_contains "$OUT" "note: conventional commits suggest" "no note when nothing differs"
}

test_nothing_releasable_without_version_fails() {
  setup_fixture
  STUB_CHANGELOG_RC=3 run_release "" --yes
  assert_status 1 "$RC" "nothing releasable exits 1"
  assert_contains "$OUT" "nothing releasable since the last tag (only docs/test/chore commits); pass VERSION to release anyway" "message explains and names the way out"
  assert_nothing_changed "nothing releasable"
}

test_nothing_releasable_with_version_goes_on_silently() {
  setup_fixture
  STUB_CHANGELOG_RC=3 run_release "" --yes 1.0.0
  assert_status 0 "$RC" "an explicit VERSION releases anyway"
  assert_not_contains "$OUT" "note:" "a failed suggestion prints no note"
}

test_differing_version_prints_a_note_and_goes_on() {
  setup_fixture
  STUB_CHANGELOG_RC=0 STUB_CHANGELOG_OUT=v1.1.0 run_release "" --yes 1.0.0
  assert_status 0 "$RC" "a differing VERSION does not block"
  assert_contains "$OUT" "note: conventional commits suggest v1.1.0" "the note names the suggestion"
  assert_contains "$OUT" "Released v1.0.0:" "the given version is released"
  STUB_CHANGELOG_RC=0 STUB_CHANGELOG_OUT=v1.0.0 run_release "" --dry-run 1.0.0
  assert_not_contains "$OUT" "note:" "a matching VERSION prints no note"
}

test_changelog_failure_without_version_is_reported() {
  setup_fixture
  STUB_CHANGELOG_RC=1 STUB_CHANGELOG_OUT="changelog: git failed" run_release "" --yes
  assert_status 1 "$RC" "a failing bin/changelog exits 1"
  assert_contains "$OUT" "cannot derive the version: changelog: git failed" "its message is passed on"
}

test_not_on_main_fails() {
  setup_fixture
  "$REAL_GIT" -C "$W" checkout -q -b feature
  run_release n 1.0.0
  assert_status 1 "$RC" "wrong branch exits 1"
  assert_contains "$OUT" "must be on main (on 'feature')" "message names the branch"
  assert_nothing_changed "wrong branch"
}

test_dirty_tree_fails() {
  setup_fixture
  echo x >>"$W/CHANGELOG.md"
  run_release n 1.0.0
  assert_status 1 "$RC" "dirty tree exits 1"
  assert_contains "$OUT" "working tree is not clean" "message explains"
  assert_eq "" "$(cat "$CALLS")" "nothing ran"
}

test_behind_origin_fails() {
  setup_fixture
  "$REAL_GIT" clone -q "$O" "$TEST_TMP/other"
  "$REAL_GIT" -C "$TEST_TMP/other" commit -q --allow-empty -m upstream
  "$REAL_GIT" -C "$TEST_TMP/other" push -q origin main
  run_release n 1.0.0
  assert_status 1 "$RC" "behind origin exits 1"
  assert_contains "$OUT" "1 commit(s) behind origin/main" "message explains"
  assert_nothing_changed "behind"
}

test_existing_local_tag_fails() {
  setup_fixture
  "$REAL_GIT" -C "$W" tag v1.0.0
  run_release n 1.0.0
  assert_status 1 "$RC" "local tag exits 1"
  assert_contains "$OUT" "tag v1.0.0 already exists locally" "message explains"
  assert_eq "" "$(grep -E '^git (commit|push)' "$CALLS" || true)" "no commit or push"
}

test_existing_remote_tag_fails() {
  setup_fixture
  "$REAL_GIT" -C "$W" tag v1.0.0
  "$REAL_GIT" -C "$W" push -q origin v1.0.0
  "$REAL_GIT" -C "$W" tag -d v1.0.0 >/dev/null
  : >"$CALLS"
  run_release n 1.0.0
  assert_status 1 "$RC" "remote-only tag exits 1"
  assert_contains "$OUT" "tag v1.0.0 already exists on origin" "message explains"
  assert_nothing_changed "remote tag"
}

test_empty_unreleased_fails() {
  setup_fixture
  printf '# Changelog\n\n## [Unreleased]\n\n## [0.9.0] - 2026-01-01\n\n- Old\n' >"$W/CHANGELOG.md"
  "$REAL_GIT" -C "$W" commit -q -am empty
  run_release n 1.0.0
  assert_status 1 "$RC" "empty Unreleased exits 1"
  assert_contains "$OUT" "'## [Unreleased]' in CHANGELOG.md is empty" "message explains"
  assert_eq "" "$(cat "$CALLS")" "lint, tests and git were not run"
}

test_gh_not_authenticated_fails() {
  setup_fixture
  RC=0
  OUT="$(cd "$W" && STUB_GH_AUTH_RC=1 PATH="$STUBS:$ORIG_PATH" "$W/bin/release" 1.0.0 2>&1 </dev/null)" || RC=$?
  assert_status 1 "$RC" "unauthenticated gh exits 1"
  assert_contains "$OUT" "gh is not authenticated" "message explains"
  assert_nothing_changed "no gh auth"
}

test_failing_lint_or_tests_abort_before_any_change() {
  setup_fixture
  RC=0
  OUT="$(cd "$W" && STUB_LINT_RC=1 PATH="$STUBS:$ORIG_PATH" "$W/bin/release" 1.0.0 2>&1 </dev/null)" || RC=$?
  assert_status 1 "$RC" "failing lint exits 1"
  assert_contains "$OUT" "bin/lint failed" "message names lint"
  assert_nothing_changed "lint failure"
  RC=0
  OUT="$(cd "$W" && STUB_TEST_RC=1 PATH="$STUBS:$ORIG_PATH" "$W/bin/release" 1.0.0 2>&1 </dev/null)" || RC=$?
  assert_status 1 "$RC" "failing tests exit 1"
  assert_contains "$OUT" "bin/test failed" "message names test"
  assert_nothing_changed "test failure"
  assert_eq "$CHANGELOG_BEFORE" "$(cat "$W/CHANGELOG.md")" "CHANGELOG.md untouched"
}

test_dry_run_changes_nothing() {
  setup_fixture
  local before
  before="$(head_sha)"
  run_release n --dry-run 1.0.0-rc.3
  assert_status 0 "$RC" "dry run exits 0"
  assert_contains "$OUT" "pre-release" "pre-release is announced"
  assert_contains "$OUT" "[dry-run] would run bin/lint" "lint step is planned"
  assert_contains "$OUT" "[dry-run] would run bin/test" "test step is planned"
  assert_contains "$OUT" "[dry-run] would move [Unreleased] under '## [1.0.0-rc.3] - 2026-02-03'" "changelog step is planned"
  assert_contains "$OUT" "- New thing" "the section body is previewed"
  assert_contains "$OUT" "[dry-run] would git commit -m 'docs: changelog for 1.0.0-rc.3'" "commit step is planned"
  assert_contains "$OUT" "[dry-run] would git tag -a v1.0.0-rc.3" "tag step is planned"
  assert_contains "$OUT" "[dry-run] would git push --atomic origin main v1.0.0-rc.3" "push step is planned"
  assert_contains "$OUT" "[dry-run] would wait for the Release workflow" "watch step is planned"
  assert_contains "$OUT" "gh release view v1.0.0-rc.3" "release URL step is planned"
  assert_eq "$before" "$(head_sha)" "HEAD unchanged"
  assert_eq "" "$("$REAL_GIT" -C "$W" status --porcelain)" "working tree unchanged"
  assert_eq "" "$("$REAL_GIT" -C "$W" tag)" "no tag"
  assert_eq "" "$(cat "$CALLS")" "nothing was run: no lint, test, commit, tag, push or gh"
}

test_dry_run_still_fails_on_bad_preconditions() {
  setup_fixture
  "$REAL_GIT" -C "$W" checkout -q -b feature
  run_release n --dry-run 1.0.0
  assert_status 1 "$RC" "dry run enforces preconditions"
}

test_yes_runs_everything_in_order_and_pushes() {
  setup_fixture
  run_release "" --yes 1.0.0-rc.3
  assert_status 0 "$RC" "release succeeds"
  assert_eq "bin/lint |bin/test |git commit|git tag|git push|gh run|gh release" "$(calls_summary)" "order: lint, test, commit, tag, push, watch, release view"
  assert_contains "$(cat "$CALLS")" "git push --atomic origin main refs/tags/v1.0.0-rc.3" "main and the tag are pushed together"
  assert_contains "$(cat "$CALLS")" "gh run watch 42" "the run found for the tag is watched"
  assert_contains "$OUT" "Released v1.0.0-rc.3: https://example.test/releases/v1.0.0-rc.3" "URL is printed"
  assert_eq "$CHANGELOG_AFTER" "$(cat "$W/CHANGELOG.md")" "CHANGELOG.md matches the golden file"
  assert_eq "docs: changelog for 1.0.0-rc.3" "$("$REAL_GIT" -C "$W" log -1 --format=%s)" "commit message"
  assert_eq "tag" "$("$REAL_GIT" -C "$W" cat-file -t v1.0.0-rc.3)" "the tag is annotated"
  assert_eq "v1.0.0-rc.3" "$("$REAL_GIT" -C "$W" tag -l --format='%(contents:subject)' v1.0.0-rc.3)" "tag message is vVERSION"
  assert_eq "$(head_sha)" "$("$REAL_GIT" -C "$O" rev-parse main)" "origin main has the changelog commit"
  assert_eq "$("$REAL_GIT" -C "$W" rev-parse 'v1.0.0-rc.3^{commit}')" "$(head_sha)" "origin has the tag on that commit"
  assert_eq "$(head_sha)" "$("$REAL_GIT" -C "$O" rev-parse 'v1.0.0-rc.3^{commit}')" "remote tag points at the commit"
}

test_release_notes_reads_the_new_section() {
  setup_fixture
  run_release "" --yes 1.0.0-rc.3
  local notes
  notes="$("$W/bin/release-notes" v1.0.0-rc.3)"
  assert_eq "### Added
- New thing" "$notes" "release-notes extracts exactly the moved content"
  run_release "" --yes 1.0.1
  assert_status 1 "$RC" "a second release with an empty Unreleased fails"
  assert_contains "$OUT" "is empty" "because Unreleased is empty now"
}

test_answer_yes_pushes() {
  setup_fixture
  run_release y 1.0.0
  assert_status 0 "$RC" "answering y releases"
  assert_contains "$OUT" "Push main and v1.0.0 to origin? [y/N]" "the question is asked"
  assert_contains "$OUT" "stable release" "stable release is announced"
  assert_contains "$(calls_summary)" "git push" "pushed"
}

test_answer_no_leaves_local_commit_and_tag() {
  setup_fixture
  run_release n 1.0.0
  assert_status 1 "$RC" "declining exits 1"
  assert_contains "$OUT" "Not pushed. The changelog commit and tag v1.0.0 exist locally only." "says what exists"
  assert_contains "$OUT" "git push --atomic origin main v1.0.0" "says how to push later"
  assert_contains "$OUT" "git tag -d v1.0.0 && git reset --hard HEAD~1" "says how to undo"
  assert_eq "" "$(grep -E '^git push' "$CALLS" || true)" "nothing was pushed"
  assert_eq "v1.0.0" "$("$REAL_GIT" -C "$W" tag)" "tag exists locally"
  assert_eq "docs: changelog for 1.0.0" "$("$REAL_GIT" -C "$W" log -1 --format=%s)" "commit exists locally"
  assert_eq "initial" "$("$REAL_GIT" -C "$O" log -1 --format=%s main)" "origin main is untouched"
  assert_eq "" "$("$REAL_GIT" -C "$O" tag)" "origin has no tag"
}

test_empty_answer_defaults_to_no() {
  setup_fixture
  run_release "" 1.0.0
  assert_status 1 "$RC" "empty answer is a no"
  assert_eq "" "$(grep -E '^git push' "$CALLS" || true)" "nothing was pushed"
}

test_failing_workflow_exits_nonzero() {
  setup_fixture
  RC=0
  OUT="$(cd "$W" && STUB_GH_WATCH_RC=1 PATH="$STUBS:$ORIG_PATH" "$W/bin/release" --yes 1.0.0 2>&1 </dev/null)" || RC=$?
  assert_status 1 "$RC" "failed workflow exits 1"
  assert_contains "$OUT" "the Release workflow failed (run 42)" "message names the run"
  assert_eq "" "$(grep '^gh release view' "$CALLS" || true)" "no URL is printed as success"
}

test_run_appearing_on_third_look_continues() {
  setup_fixture
  BBAPPROVE_RELEASE_RUN_WAIT_SECONDS=2 BBAPPROVE_RELEASE_RUN_POLL_SECONDS=1 STUB_GH_RUN_APPEARS_ON=3 run_release "" --yes 1.0.0
  assert_status 0 "$RC" "run found on the third look: release succeeds"
  assert_eq "3" "$(cat "$CALLS.runlist")" "looked for the run three times"
  assert_eq "1" "$(grep -c 'waiting for the Release workflow run to appear' <<<"$OUT")" "progress note is printed once"
  assert_contains "$(cat "$CALLS")" "gh run watch 42" "the run found on the third look is watched"
  assert_contains "$OUT" "Released v1.0.0: https://example.test/releases/v1.0.0" "URL is printed"
}

test_run_found_at_once_prints_no_wait_note() {
  setup_fixture
  STUB_GH_RUN_APPEARS_ON=1 run_release "" --yes 1.0.0
  assert_status 0 "$RC" "run found on the first look: release succeeds"
  assert_eq "" "$(grep 'waiting for the Release workflow run' <<<"$OUT" || true)" "no progress note"
  assert_eq "1" "$(cat "$CALLS.runlist")" "looked once"
}

test_run_never_appearing_gives_up_after_the_wait() {
  setup_fixture
  BBAPPROVE_RELEASE_RUN_WAIT_SECONDS=2 BBAPPROVE_RELEASE_RUN_POLL_SECONDS=1 STUB_GH_RUN_APPEARS_ON=99 run_release "" --yes 1.0.0
  assert_status 1 "$RC" "run never appears: exits 1"
  assert_contains "$OUT" "pushed v1.0.0, but no Release workflow run appeared after 2 s; the last \`gh run list\` exited 0 with no run" "says the lookup succeeded but found nothing"
  assert_contains "$OUT" "by hand: gh run list --workflow Release --branch v1.0.0" "gives the command to look by hand"
  assert_contains "$OUT" "Actions: the Actions tab of the repository" "falls back when the origin URL is a local path"
  assert_eq "3" "$(cat "$CALLS.runlist")" "one look plus two polls within the wait"
  assert_eq "" "$(grep '^gh run watch' "$CALLS" || true)" "nothing is watched"
  assert_eq "" "$(grep '^gh release view' "$CALLS" || true)" "no URL is printed"
}

test_run_list_errors_are_reported_and_polling_continues() {
  setup_fixture
  BBAPPROVE_RELEASE_RUN_WAIT_SECONDS=2 BBAPPROVE_RELEASE_RUN_POLL_SECONDS=1 STUB_GH_RUN_LIST_ERR="HTTP 503: try again later" run_release "" --yes 1.0.0
  assert_status 1 "$RC" "failing lookups exit 1"
  assert_eq "3" "$(cat "$CALLS.runlist")" "a failing look is polled again until the wait is used up"
  assert_contains "$OUT" "the last \`gh run list\` exited 1: HTTP 503: try again later" "the last gh error is in the message"
  assert_contains "$OUT" "by hand: gh run list --workflow Release --branch v1.0.0" "gives the command to look by hand"
  assert_eq "" "$(grep '^gh run watch' "$CALLS" || true)" "nothing is watched"
}

test_run_found_on_second_look_succeeds() {
  setup_fixture
  BBAPPROVE_RELEASE_RUN_WAIT_SECONDS=2 BBAPPROVE_RELEASE_RUN_POLL_SECONDS=1 STUB_GH_RUN_APPEARS_ON=2 run_release "" --yes 1.0.0
  assert_status 0 "$RC" "a run found after an empty look succeeds"
  assert_eq "2" "$(cat "$CALLS.runlist")" "looked twice"
  assert_eq "" "$(grep 'exited' <<<"$OUT" || true)" "no failure note on a release that succeeds"
}

test_run_never_appearing_with_zero_wait_looks_once() {
  setup_fixture
  STUB_GH_RUN_APPEARS_ON=99 run_release "" --yes 1.0.0
  assert_status 1 "$RC" "zero wait: exits 1 without waiting"
  assert_eq "1" "$(cat "$CALLS.runlist")" "looked once"
  assert_eq "" "$(grep 'waiting for the Release workflow run' <<<"$OUT" || true)" "no progress note"
}

# Tests share no state (each has its own temp dir), so they run in parallel; BASH_TEST_JOBS=1 runs them serially.
BASH_TEST_JOBS="${BASH_TEST_JOBS:-8}"
build_template
run_tests "$@"
