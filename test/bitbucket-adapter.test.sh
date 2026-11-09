#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ADAPTER="$PROJECT_ROOT/adapters/bitbucket"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
STUB_BIN=""; OP_BIN=""; STUB_PATH=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; return 0; }
cleanup_suite_tmp() { cleanup_test_tmp; [[ -z "$STUB_BIN" ]] || rm -rf "$STUB_BIN" "$OP_BIN"; return 0; }
trap 'cleanup_suite_tmp' EXIT

URL="https://bitbucket.example.com/projects/PROJ/repos/repo/pull-requests/5"
RAW='{"id":5,"version":3,"title":"chore(deps): update x to v1.0.1","author":{"user":{"emailAddress":"bot@example.com","slug":"bot"}},"fromRef":{"id":"refs/heads/renovate/x","displayId":"renovate/x","latestCommit":"abc","repository":{"slug":"repo","project":{"key":"PROJ"}}},"toRef":{"id":"refs/heads/main","repository":{"slug":"repo","project":{"key":"PROJ"}}},"participants":[],"reviewers":[{"user":{"slug":"me"},"status":"APPROVED"},{"user":{"slug":"bob"},"status":"NEEDS_WORK"}],"properties":{"qgStatus":"PASSED","commentCount":4,"openTaskCount":1}}'

# The curl (and op) stubs are the fixed ones in test/fixtures, linked once per suite and driven by env:
# STUB_LOG (per-test call log), STUB_RAW (canned PR) and the STUB_*_FAIL switches. Nothing is written per test.
STUB_BIN=""
setup_stub() {
  TEST_TMP="$(mktemp -d)"
  if [[ -z "$STUB_BIN" ]]; then
    STUB_BIN="$(mktemp -d)"; use_stubs "$STUB_BIN" curl
    OP_BIN="$(mktemp -d)"; use_stubs "$OP_BIN" curl op
  fi
  export BBAPPROVE_BASE_URL="https://bitbucket.example.com" STUB_LOG="$TEST_TMP/curl.log" STUB_RAW="$RAW" BBAPPROVE_TOKEN=t BBAPPROVE_RETRY_SLEEP=0
  : >"$STUB_LOG"
  STUB_PATH="$STUB_BIN"
}

run_adapter() { PATH="$STUB_PATH:$PATH" "$ADAPTER" "$@" 2>&1; }

test_pr_view_normalizes() {
  setup_stub
  local out; out="$(run_adapter pr view "$URL")"
  assert_eq "$(jq -r '.url' <<<"$out")" "$URL" "url"
  assert_eq "$(jq -r '"\(.project)/\(.repo)#\(.number)"' <<<"$out")" "PROJ/repo#5" "project/repo#number"
  assert_eq "$(jq -r '.author.email' <<<"$out")" "bot@example.com" "author email"
  assert_eq "$(jq -r '.headBranch' <<<"$out")" "renovate/x" "head branch"
  assert_eq "$(jq -r '.approvedByMe' <<<"$out")" "true" "approvedByMe from whoami + reviewers"
  assert_eq "$(jq -r '.build' <<<"$out")" "passed" "build from batched build-status stats"
  assert_eq "$(jq -r '.commentCount' <<<"$out")" "1" "view: comments exclude tasks (3 comments - 2 task comments)"
  assert_eq "$(jq -r '.openTasks' <<<"$out")" "1" "view: open tasks from blocker-comments"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_list_returns_normalized_array() {
  setup_stub
  local out; out="$(run_adapter pr list --reviewer @me --state open)"
  assert_eq "$(jq -r 'length' <<<"$out")" "1" "one PR"
  assert_eq "$(jq -r '.[0].number' <<<"$out")" "5" "number"
  assert_eq "$(jq -r '.[0].build' <<<"$out")" "passed" "build passed"
  assert_eq "$(jq -r '.[0].qualityGate' <<<"$out")" "passed" "quality gate from qgStatus"
  assert_eq "$(jq -r '"\(.[0].approvals) \(.[0].needsWork)"' <<<"$out")" "1 1" "approvals and needs-work counted from reviewers"
  assert_eq "$(jq -r '"\(.[0].commentCount) \(.[0].openTasks)"' <<<"$out")" "4 1" "comment and open task counts"
  assert_contains "$(cat "$STUB_LOG")" "withProperties=true" "list asks for PR properties"
  cleanup_test_tmp; TEST_TMP=""
}

test_curl_timeouts() {
  setup_stub
  run_adapter pr view "$URL" >/dev/null
  assert_contains "$(head -n1 "$STUB_LOG")" "--connect-timeout 2.7 --max-time 3.5" "timeouts on every call"
  cleanup_test_tmp; TEST_TMP=""
}

test_review_and_merge() {
  setup_stub
  run_adapter pr review "$URL" --approve >/dev/null
  run_adapter pr merge "$URL" >/dev/null
  assert_contains "$(cat "$STUB_LOG")" "pull-requests/5/approve" "approve called"
  assert_contains "$(cat "$STUB_LOG")" "pull-requests/5/merge?version=3" "merge uses fresh version"
  cleanup_test_tmp; TEST_TMP=""
}

test_merge_deletes_source_branch() {
  setup_stub
  run_adapter pr merge "$URL" >/dev/null
  assert_contains "$(cat "$STUB_LOG")" "-X DELETE" "merge deletes the source branch by default"
  assert_contains "$(cat "$STUB_LOG")" "/rest/branch-utils/1.0/projects/PROJ/repos/repo/branches" "branch-utils endpoint"
  assert_eq "$(jq -c '.' "$STUB_LOG.body")" '{"name":"refs/heads/renovate/x","dryRun":false}' "delete body names the source ref"
  : >"$STUB_LOG"
  BBAPPROVE_DELETE_SOURCE_BRANCH=false run_adapter pr merge "$URL" >/dev/null
  assert_not_contains "$(cat "$STUB_LOG")" "DELETE" "BBAPPROVE_DELETE_SOURCE_BRANCH=false keeps the branch"
  local out rc=0
  out="$(STUB_DELETE_FAIL=1 run_adapter pr merge "$URL")" || rc=$?
  assert_eq "$rc" "0" "a failed branch deletion does not fail the merge"
  assert_contains "$out" "merge ok, but could not delete branch renovate/x (HTTP 500)" "deletion failure is a warning"
  cleanup_test_tmp; TEST_TMP=""
}

test_merge_retries_when_stale() {
  setup_stub
  STUB_STALE_FILE="$TEST_TMP/stale" run_adapter pr merge "$URL" >/dev/null
  assert_eq "$(grep -c '/merge' "$STUB_LOG")" "2" "merge attempted twice"
  cleanup_test_tmp; TEST_TMP=""
}

test_failures_exit_nonzero_with_message() {
  setup_stub
  local out rc=0
  out="$(STUB_MERGE_FAIL=1 run_adapter pr merge "$URL")" || rc=$?
  assert_eq "$rc" "1" "merge failure exits 1"
  assert_contains "$out" "checks failed" "merge error message"
  rc=0; out="$(STUB_APPROVE_FAIL=1 run_adapter pr review "$URL" --approve)" || rc=$?
  assert_contains "$out" "approve HTTP 403" "approve error message"
  rc=0; out="$(run_adapter pr view "not-a-url")" || rc=$?
  assert_eq "$rc" "2" "bad URL exits 2"
  assert_contains "$out" "Not a PR URL" "bad URL message"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_comment_posts_text() {
  setup_stub
  run_adapter pr comment "$URL" --body "looks good" >/dev/null
  assert_contains "$(cat "$STUB_LOG")" "-X POST" "comment uses POST"
  assert_contains "$(cat "$STUB_LOG")" "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests/5/comments" "comments endpoint"
  assert_eq "$(jq -r '.text' "$STUB_LOG.body")" "looks good" "body is {text: TEXT}"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_comment_special_chars_are_valid_json() {
  setup_stub
  local text=$'say "hi" \\ back\nnew line — ✓ é'
  run_adapter pr comment "$URL" --body "$text" >/dev/null
  jq -e . "$STUB_LOG.body" >/dev/null || test_fail "comment body is not valid JSON"
  assert_eq "$(jq -r '.text' "$STUB_LOG.body")" "$text" "quotes, backslash, newline and unicode survive"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_comment_empty_body_exits_2() {
  setup_stub
  local rc=0 out
  out="$(run_adapter pr comment "$URL" --body "   ")" || rc=$?
  assert_eq "$rc" "2" "empty body exits 2"
  assert_contains "$out" "empty comment" "empty body message"
  rc=0; out="$(run_adapter pr comment "$URL")" || rc=$?
  assert_eq "$rc" "2" "missing body exits 2"
  assert_eq "$(grep -c comments "$STUB_LOG" || true)" "0" "no comment request sent"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_comment_body_file() {
  setup_stub
  printf 'from "file"\nsecond line\n' >"$TEST_TMP/body.txt"
  run_adapter pr comment "$URL" --body-file "$TEST_TMP/body.txt" >/dev/null
  assert_eq "$(jq -r '.text' "$STUB_LOG.body")" $'from "file"\nsecond line' "--body-file content is posted"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_comment_failure_exits_1() {
  setup_stub
  local rc=0 out
  out="$(STUB_COMMENT_FAIL=1 run_adapter pr comment "$URL" --body "x")" || rc=$?
  assert_eq "$rc" "1" "comment failure exits 1"
  assert_contains "$out" "comment HTTP 500" "comment failure message with HTTP code"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_close_declines_with_fresh_version() {
  setup_stub
  STUB_RAW="${RAW/\"version\":3/\"version\":7}" run_adapter pr close "$URL" >/dev/null
  assert_contains "$(cat "$STUB_LOG")" "pull-requests/5/decline?version=7" "decline uses fresh version"
  assert_eq "$(jq -r '.version' "$STUB_LOG.body")" "7" "decline body carries version"
  assert_eq "$(grep -c '/comments' "$STUB_LOG" || true)" "0" "no comment without --comment"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_close_comments_before_decline() {
  setup_stub
  run_adapter pr close "$URL" --comment "closing: superseded" >/dev/null
  local order
  order="$(awk '/\/comments/ {c = NR} /\/decline/ {d = NR} END {print (c && d && c < d) ? "comment-then-decline" : "wrong-order"}' "$STUB_LOG")"
  assert_eq "$order" "comment-then-decline" "comment is posted before decline"
  assert_eq "$(jq -r '.version' "$STUB_LOG.body")" "3" "decline body carries the fresh version after the comment"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_close_failed_comment_prevents_decline() {
  setup_stub
  local rc=0 out
  out="$(STUB_COMMENT_FAIL=1 run_adapter pr close "$URL" --comment "bye")" || rc=$?
  assert_eq "$rc" "1" "close exits 1 when the comment fails"
  assert_contains "$out" "close aborted" "close abort message"
  assert_not_contains "$(cat "$STUB_LOG")" "decline" "no decline call after failed comment"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_close_decline_failure_exits_1() {
  setup_stub
  local rc=0 out
  out="$(STUB_DECLINE_FAIL=1 run_adapter pr close "$URL")" || rc=$?
  assert_eq "$rc" "1" "decline failure exits 1"
  assert_contains "$out" "decline HTTP 409: PR is out of date" "decline failure message with HTTP code and errors"
  cleanup_test_tmp; TEST_TMP=""
}

test_missing_base_url_exits_2() {
  setup_stub
  local rc=0 out
  out="$(BBAPPROVE_BASE_URL="" run_adapter pr list --reviewer @me --state open)" || rc=$?
  assert_eq "$rc" "2" "missing BBAPPROVE_BASE_URL exits 2"
  assert_eq "$out" "bitbucket adapter: BBAPPROVE_BASE_URL is not set" "no built-in default URL"
  assert_eq "$(cat "$STUB_LOG")" "" "no curl call without a base URL"
  cleanup_test_tmp; TEST_TMP=""
}

# Stub op: logs its argv; OP_TOKEN_EMPTY=1 makes label=token fail, OP_ALL_FAIL=1 makes every lookup fail.
setup_op_stub() {
  setup_stub
  STUB_PATH="$OP_BIN"
}

test_auth_token_uses_token_name_item() {
  setup_op_stub
  local out
  out="$(BBAPPROVE_TOKEN="" BBAPPROVE_TOKEN_NAME="MY_ITEM" run_adapter auth token)"
  assert_contains "$out" "Waiting for 1Password: op item get MY_ITEM" "message names the configured item"
  assert_contains "$out" "tok-from-op" "token printed"
  assert_contains "$(cat "$STUB_LOG.op")" 'item get MY_ITEM --reveal --fields label=token' "op asked for the configured item"
  assert_eq "$(grep -c credential "$STUB_LOG.op" || true)" "0" "no credential retry when token worked"
  cleanup_test_tmp; TEST_TMP=""
}

test_auth_token_falls_back_to_credential_field() {
  setup_op_stub
  local out
  out="$(OP_TOKEN_EMPTY=1 BBAPPROVE_TOKEN="" BBAPPROVE_TOKEN_NAME="MY_ITEM" run_adapter auth token)"
  assert_contains "$out" "tok-from-credential" "credential field used when token field fails"
  assert_contains "$(cat "$STUB_LOG.op")" 'item get MY_ITEM --reveal --fields label=credential' "op retried with label=credential"
  cleanup_test_tmp; TEST_TMP=""
}

test_auth_token_env_wins_and_op_not_called() {
  setup_op_stub
  local out
  out="$(BBAPPROVE_TOKEN="env-secret" BBAPPROVE_TOKEN_NAME="MY_ITEM" run_adapter auth token)"
  assert_eq "$out" "env-secret" "BBAPPROVE_TOKEN printed, no notice"
  assert_eq "$([[ -e "$STUB_LOG.op" ]] && echo yes || echo no)" "no" "op was not called"
  cleanup_test_tmp; TEST_TMP=""
}

test_auth_token_no_token_no_name_fails() {
  setup_op_stub
  local rc=0 out
  out="$(BBAPPROVE_TOKEN="" BBAPPROVE_TOKEN_NAME="" run_adapter auth token)" || rc=$?
  assert_eq "$rc" "1" "exit 1"
  assert_contains "$out" "set BBAPPROVE_TOKEN, or BBAPPROVE_TOKEN_NAME" "help message"
  assert_eq "$([[ -e "$STUB_LOG.op" ]] && echo yes || echo no)" "no" "op was not called without a name"
  cleanup_test_tmp; TEST_TMP=""
}

test_auth_token_item_lookup_fails() {
  setup_op_stub
  local rc=0 out
  out="$(OP_ALL_FAIL=1 BBAPPROVE_TOKEN="" BBAPPROVE_TOKEN_NAME="MY_ITEM" run_adapter auth token)" || rc=$?
  assert_eq "$rc" "1" "exit 1 when neither field yields a token"
  assert_contains "$out" "No token" "message"
  cleanup_test_tmp; TEST_TMP=""
}

test_pr_commands_still_require_token() {
  setup_op_stub
  local rc=0 out
  out="$(BBAPPROVE_TOKEN="" BBAPPROVE_TOKEN_NAME="MY_ITEM" run_adapter pr list --reviewer @me --state open)" || rc=$?
  assert_eq "$rc" "2" "pr commands exit 2 without BBAPPROVE_TOKEN"
  assert_eq "$out" "bitbucket adapter: BBAPPROVE_TOKEN is not set" "message"
  assert_eq "$(cat "$STUB_LOG")" "" "no curl call without a token"
  cleanup_test_tmp; TEST_TMP=""
}

run_tests "$@"
