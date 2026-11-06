#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_UNDER_TEST="$PROJECT_ROOT/bin/bbapprove"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; return 0; }
trap 'cleanup_test_tmp' EXIT

BOT="renovate-bot@example.com"

# pr <id> <email> <title> <branch>
pr_json() {
  printf '{"id":%s,"version":3,"title":"%s","author":{"user":{"emailAddress":"%s"}},"fromRef":{"displayId":"%s"},"toRef":{"repository":{"slug":"repo","project":{"key":"PROJ"}}},"participants":[]}' "$1" "$3" "$2" "$4"
}

setup_stub() {
  TEST_TMP="$(mktemp -d)"
  mkdir -p "$TEST_TMP/bin"
  {
    printf '[%s,%s,%s,%s,%s,%s]' \
      "$(pr_json 1 "$BOT" 'fix(deps): update dependency org.jsoup:jsoup to v1.24.1 (master)' renovate/jsoup)" \
      "$(pr_json 2 "$BOT" 'chore(deps): update dependency some-library to v1.2.3 (master)' renovate/some-library)" \
      "$(pr_json 3 "$BOT" 'chore(deps): update dependency foo to v4 (master)' renovate/major-foo)" \
      "$(pr_json 4 "someone@example.com" 'chore(deps): update dependency bar to v1.0.1' renovate/bar)" \
      "$(pr_json 5 "$BOT" 'feat: something' renovate/baz)" \
      "$(pr_json 6 "$BOT" 'chore(deps): update postgres docker digest to 123b179 (master)' renovate/pg)"
  } >"$TEST_TMP/prs.json"
  cat >"$TEST_TMP/bin/curl" <<'STUB'
#!/usr/bin/env bash
url="${!#}"
echo "$*" >>"$STUB_LOG"
if [[ "$url" == *whoami* ]]; then printf 'me\n200'
elif [[ "$url" == *dashboard/pull-requests* ]]; then printf '{"isLastPage":true,"values":%s}\n200' "$(cat "$STUB_PRS")"
elif [[ "$url" == *"/merge"* && -n "${STUB_MERGE_FAIL:-}" ]]; then printf '{"errors":[{"message":"checks failed"}]}\n409'
elif [[ "$url" == *"/merge"* && -n "${STUB_STALE_FILE:-}" && ! -e "$STUB_STALE_FILE" ]]; then touch "$STUB_STALE_FILE"; printf '{"errors":[{"message":"modify a pull request based on out-of-date information."}]}\n409'
elif [[ "$url" =~ pull-requests/[0-9]+$ ]]; then printf '{"version":7}\n200'
else printf '{}\n200'; fi
STUB
  chmod +x "$TEST_TMP/bin/curl"
  export BBAPPROVE_RETRY_SLEEP=0 STUB_LOG="$TEST_TMP/curl.log" STUB_PRS="$TEST_TMP/prs.json" BBAPPROVE_TOKEN=t
  : >"$STUB_LOG"
}

make_fzf_stub() { # picks lines whose text matches $STUB_FZF_PICK (regex); records stdin
  cat >"$TEST_TMP/bin/fzf" <<'STUB'
#!/usr/bin/env bash
cat >"$STUB_FZF_INPUT"
grep -E "${STUB_FZF_PICK:-^$}" "$STUB_FZF_INPUT" || exit 1
STUB
  chmod +x "$TEST_TMP/bin/fzf"
  export STUB_FZF_INPUT="$TEST_TMP/fzf.in"
}

run_script() { PATH="$TEST_TMP/bin:$PATH" "$SCRIPT_UNDER_TEST" "$@" 2>&1; }

test_dry_run_classifies() {
  setup_stub
  local out; out="$(run_script)"
  assert_contains "$out" "ELIGIBLE PROJ/repo#1" "jsoup patch is eligible"
  assert_contains "$out" "#2 chore(deps): update dependency some-library to v1.2.3 (master) — disallowed dependency 'some-library'" "some-library disallowed"
  assert_contains "$out" "#3 chore(deps): update dependency foo to v4 (master) — major update" "major skipped"
  assert_contains "$out" "not the renovate bot" "wrong author skipped"
  assert_contains "$out" "subject does not start" "bad subject skipped"
  assert_contains "$out" "ELIGIBLE PROJ/repo#6" "digest update is eligible"
  assert_not_contains "$(cat "$STUB_LOG")" "POST" "dry run makes no POST"
  cleanup_test_tmp; TEST_TMP=""
}

test_apply_approves_and_merges() {
  setup_stub
  local out; out="$(run_script --apply)"
  assert_contains "$out" "MERGED  PROJ/repo#1" "merged #1"
  assert_contains "$(cat "$STUB_LOG")" "pull-requests/1/approve" "approve called"
  assert_contains "$(cat "$STUB_LOG")" "pull-requests/1/merge?version=7" "merge uses freshly fetched version"
  assert_not_contains "$(cat "$STUB_LOG")" "pull-requests/2/approve" "disallowed never approved"
  cleanup_test_tmp; TEST_TMP=""
}

test_merge_failure_reported() {
  setup_stub
  local out; out="$(STUB_MERGE_FAIL=1 run_script --apply)"
  assert_contains "$out" "APPROVED, NOT MERGED PROJ/repo#1" "failed merge reported"
  assert_contains "$out" "checks failed" "merge error message shown"
  cleanup_test_tmp; TEST_TMP=""
}

test_stale_version_retried() {
  setup_stub
  local out; out="$(STUB_STALE_FILE="$TEST_TMP/stale" run_script --apply)"
  assert_contains "$out" "MERGED  PROJ/repo#1" "merge succeeds after out-of-date retry"
  assert_not_contains "$out" "NOT MERGED" "no PR left unmerged after retry"
  assert_eq "$(grep -c 'pull-requests/1/merge' "$STUB_LOG")" "2" "#1 merge attempted twice"
  cleanup_test_tmp; TEST_TMP=""
}

test_interactive_only_picked_are_merged() {
  setup_stub; make_fzf_stub
  local out; out="$(STUB_FZF_PICK='PROJ/repo#6' run_script -i)"
  assert_contains "$out" "MERGED  PROJ/repo#6" "picked PR merged"
  assert_not_contains "$(cat "$STUB_LOG")" "pull-requests/1/approve" "unpicked PR untouched"
  assert_contains "$(cat "$STUB_FZF_INPUT")" "PROJ/repo#1" "eligible PR offered in picker"
  assert_not_contains "$(cat "$STUB_FZF_INPUT")" "some-library" "ineligible PR not offered"
  cleanup_test_tmp; TEST_TMP=""
}

test_interactive_cancel_does_nothing() {
  setup_stub; make_fzf_stub
  local out; out="$(run_script -i)"
  assert_contains "$out" "Cancelled." "cancel reported"
  assert_not_contains "$(cat "$STUB_LOG")" "approve" "nothing approved on cancel"
  cleanup_test_tmp; TEST_TMP=""
}

test_custom_disallow() {
  setup_stub
  local out; out="$(run_script --disallow some-library,jsoup)"
  assert_contains "$out" "disallowed dependency 'jsoup'" "custom disallow honored"
  cleanup_test_tmp; TEST_TMP=""
}

run_tests "$@"
