#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ADAPTER="$PROJECT_ROOT/adapters/bitbucket"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; return 0; }
trap 'cleanup_test_tmp' EXIT

URL="https://bitbucket.example.com/projects/PROJ/repos/repo/pull-requests/5"
RAW='{"id":5,"version":3,"title":"chore(deps): update x to v1.0.1","author":{"user":{"emailAddress":"bot@x","slug":"bot"}},"fromRef":{"displayId":"renovate/x","latestCommit":"abc"},"toRef":{"repository":{"slug":"repo","project":{"key":"PROJ"}}},"participants":[],"reviewers":[{"user":{"slug":"me"},"status":"APPROVED"},{"user":{"slug":"bob"},"status":"NEEDS_WORK"}],"properties":{"qgStatus":"PASSED","commentCount":4,"openTaskCount":1}}'

setup_stub() {
  TEST_TMP="$(mktemp -d)"; mkdir -p "$TEST_TMP/bin"
  cat >"$TEST_TMP/bin/curl" <<'STUB'
#!/usr/bin/env bash
url="${!#}"
echo "$*" >>"$STUB_LOG"
if [[ "$url" == *whoami* ]]; then printf 'me\n200'
elif [[ "$url" == *build-status*/commits/stats ]]; then printf '{"abc":{"successful":2,"failed":0,"inProgress":0}}\n200'
elif [[ "$url" == *"/activities"* ]]; then printf '{"values":[{"action":"COMMENTED"},{"action":"COMMENTED"},{"action":"OPENED"}]}\n200'
elif [[ "$url" == *dashboard/pull-requests* ]]; then printf '{"isLastPage":true,"values":[%s]}\n200' "$STUB_RAW"
elif [[ "$url" == *"/merge"* && -n "${STUB_STALE_FILE:-}" && ! -e "$STUB_STALE_FILE" ]]; then touch "$STUB_STALE_FILE"; printf '{"errors":[{"message":"out-of-date"}]}\n409'
elif [[ "$url" == *"/merge"* && -n "${STUB_MERGE_FAIL:-}" ]]; then printf '{"errors":[{"message":"checks failed"}]}\n409'
elif [[ "$url" == *"/approve" && -n "${STUB_APPROVE_FAIL:-}" ]]; then printf '{}\n403'
elif [[ "$url" =~ pull-requests/[0-9]+$ ]]; then printf '%s\n200' "$STUB_RAW"
else printf '{}\n200'; fi
STUB
  chmod +x "$TEST_TMP/bin/curl"
  export STUB_LOG="$TEST_TMP/curl.log" STUB_RAW="$RAW" BBAPPROVE_TOKEN=t BBAPPROVE_RETRY_SLEEP=0
  : >"$STUB_LOG"
}

run_adapter() { PATH="$TEST_TMP/bin:$PATH" "$ADAPTER" "$@" 2>&1; }

test_pr_view_normalizes() {
  setup_stub
  local out; out="$(run_adapter pr view "$URL")"
  assert_eq "$(jq -r '.url' <<<"$out")" "$URL" "url"
  assert_eq "$(jq -r '"\(.project)/\(.repo)#\(.number)"' <<<"$out")" "PROJ/repo#5" "project/repo#number"
  assert_eq "$(jq -r '.author.email' <<<"$out")" "bot@x" "author email"
  assert_eq "$(jq -r '.headBranch' <<<"$out")" "renovate/x" "head branch"
  assert_eq "$(jq -r '.approvedByMe' <<<"$out")" "true" "approvedByMe from whoami + reviewers"
  assert_eq "$(jq -r '.build' <<<"$out")" "passed" "build from batched build-status stats"
  assert_eq "$(jq -r '.commentCount' <<<"$out")" "2" "view takes comment count from activities"
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

run_tests "$@"
