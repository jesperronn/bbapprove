#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
ADAPTER="$PROJECT_ROOT/adapters/demo"
REAL_VIEW_GOLDEN="$PROJECT_ROOT/internal/bbfake/testdata/adapter/view_basic.golden.json"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
trap 'cleanup_test_tmp' EXIT

URL_101="https://bitbucket.example.com/projects/ACME/repos/shop/pull-requests/101"
URL_105="https://bitbucket.example.com/projects/ACME/repos/shop/pull-requests/105"

# Every test starts from the built-in data with an empty state file in a temp dir.
setup_state() {
  cleanup_test_tmp
  TEST_TMP="$(mktemp -d)"
  export BBAPPROVE_DEMO_STATE="$TEST_TMP/state"
}

# run_adapter ARGS: sets OUT (stdout and stderr) and RC (exit status).
run_adapter() {
  RC=0
  OUT="$("$ADAPTER" "$@" 2>&1)" || RC=$?
}

test_auth_token_prints_demo_token() {
  setup_state
  run_adapter auth token
  assert_status 0 "$RC" "auth token exits 0"
  assert_eq "$OUT" "demo-token" "auth token prints demo-token"
}

test_list_shape_matches_real_adapter() {
  setup_state
  run_adapter pr list --reviewer @me --state open
  assert_status 0 "$RC" "pr list exits 0"
  local real_keys demo_keys
  real_keys="$(jq -r '.stdout' "$REAL_VIEW_GOLDEN" | jq -c 'keys_unsorted')"
  demo_keys="$(jq -c '.[0] | keys_unsorted' <<<"$OUT")"
  assert_eq "$demo_keys" "$real_keys" "list item keys and order match the real adapter's view output"
  assert_eq "$(jq -c '.[0].author | keys_unsorted' <<<"$OUT")" '["email","login"]' "author keys match"
  assert_eq "$(jq -r 'length' <<<"$OUT")" "8" "eight fictional open PRs"
  assert_eq "$(jq -r '[.[].build] | sort | unique | join(",")' <<<"$OUT")" "failed,passed,running" "builds cover passed, failed and running"
}

test_view_shape_matches_real_adapter() {
  setup_state
  run_adapter pr view "$URL_101"
  assert_status 0 "$RC" "pr view exits 0"
  local real_keys demo_keys
  real_keys="$(jq -r '.stdout' "$REAL_VIEW_GOLDEN" | jq -c 'keys_unsorted')"
  demo_keys="$(jq -c 'keys_unsorted' <<<"$OUT")"
  assert_eq "$demo_keys" "$real_keys" "view keys and order match the real adapter"
  assert_eq "$(jq -r '.url' <<<"$OUT")" "$URL_101" "view url is the normalized PR url"
}

test_approve_then_list_shows_it() {
  setup_state
  run_adapter pr review "$URL_101" --approve
  assert_status 0 "$RC" "approve exits 0 and prints nothing"
  assert_eq "$OUT" "" "approve prints nothing on success"
  run_adapter pr list --reviewer @me --state open
  assert_eq "$(jq -r '.[] | select(.number==101) | "\(.approvedByMe) \(.approvals)"' <<<"$OUT")" "true 1" "list shows approval by me and one approval"
}

test_merge_after_approval_removes_pr() {
  setup_state
  run_adapter pr merge "$URL_101"
  assert_status 1 "$RC" "merge without approval is refused"
  assert_contains "$OUT" "Needs 1 more approval" "refusal names the missing approval"
  run_adapter pr review "$URL_101" --approve
  run_adapter pr merge "$URL_101"
  assert_status 0 "$RC" "merge after approval exits 0"
  run_adapter pr list --reviewer @me --state open
  assert_eq "$(jq -r 'length' <<<"$OUT")" "7" "merged PR leaves the list"
  run_adapter pr view "$URL_101"
  assert_status 1 "$RC" "a merged PR is no longer open"
}

test_merge_refused_while_build_fails() {
  setup_state
  run_adapter pr merge "$URL_105"
  assert_status 1 "$RC" "merge of a failing build exits 1"
  assert_contains "$OUT" "builds are successful" "refusal names the failing build"
}

test_decline_removes_pr() {
  setup_state
  run_adapter pr close "$URL_105" --comment "not now"
  assert_status 0 "$RC" "close exits 0"
  run_adapter pr list --reviewer @me --state open
  assert_eq "$(jq -r 'length' <<<"$OUT")" "7" "declined PR leaves the list"
}

test_comment_checks_body_and_pr() {
  setup_state
  run_adapter pr comment "$URL_101" --body "looks fine"
  assert_status 0 "$RC" "comment exits 0"
  run_adapter pr comment "$URL_101" --body "   "
  assert_status 2 "$RC" "blank comment exits 2"
  assert_contains "$OUT" "empty comment" "blank comment message"
  run_adapter pr comment "https://bitbucket.example.com/projects/ACME/repos/shop/pull-requests/999" --body "x"
  assert_status 1 "$RC" "comment on an unknown PR exits 1"
}

test_state_stays_in_the_temp_file() {
  setup_state
  run_adapter pr review "$URL_101" --approve
  assert_contains "$(cat "$BBAPPROVE_DEMO_STATE")" "approved 101" "approval is written to BBAPPROVE_DEMO_STATE"
  rm -f "$BBAPPROVE_DEMO_STATE"
  run_adapter pr list --reviewer @me --state open
  assert_eq "$(jq -r '.[] | select(.number==101) | .approvedByMe' <<<"$OUT")" "false" "removing the state file resets the data"
}

test_usage_errors_exit_2() {
  setup_state
  run_adapter bogus
  assert_status 2 "$RC" "unknown command exits 2"
  run_adapter pr frobnicate
  assert_status 2 "$RC" "unknown pr subcommand exits 2"
  run_adapter pr list --reviewer someone --state open
  assert_status 2 "$RC" "list with other arguments exits 2"
  run_adapter pr view "not-a-url"
  assert_status 2 "$RC" "bad URL exits 2"
  assert_contains "$OUT" "Not a PR URL" "bad URL message"
  run_adapter pr review "$URL_101"
  assert_status 2 "$RC" "review without --approve exits 2"
}

test_no_real_hosts_or_people() {
  local files hosts mails
  files=("$PROJECT_ROOT/adapters/demo" "$PROJECT_ROOT/bin/demo" "$PROJECT_ROOT/test/demo-adapter.test.sh")
  while IFS= read -r f; do files+=("$f"); done < <(find "$PROJECT_ROOT/demo" -type f ! -name '*.gif' ! -name '*.svg' 2>/dev/null)
  hosts="$(grep -hoE 'https?://[^/[:space:]"'\''\\]+' "${files[@]}" 2>/dev/null | sed -E 's#^https?://##' | sort -u || true)"
  # www.w3.org is the SVG XML namespace named in demo/cast2svg, not a host that is contacted.
  assert_eq "$(grep -vE '(^|\.)example\.com$|^www\.w3\.org$' <<<"$hosts" || true)" "" "every host is example.com (checked: $(tr '\n' ' ' <<<"$hosts"))"
  mails="$(grep -hoE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+' "${files[@]}" 2>/dev/null | sort -u || true)"
  assert_eq "$(grep -vE '@(example\.com|acme\.test)$' <<<"$mails" || true)" "" "every email is at example.com or acme.test"
}

run_tests "$@"
