#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

test_normalize_time_keeps_a_dot_radix() {
  assert_eq "1731000000.123456" "$(bash_test_normalize_time 1731000000.123456)" "a dot radix is unchanged"
}

test_normalize_time_replaces_a_comma_radix() {
  assert_eq "1731000000.123456" "$(bash_test_normalize_time 1731000000,123456)" "a comma radix becomes a dot"
}

test_time_line_reports_a_dot_under_a_comma_locale() {
  local loc out
  loc="$(locale -a 2>/dev/null | grep -i -m1 -E '^(da_DK|de_DE|fr_FR)' || true)"
  if [[ -z "$loc" ]]; then
    test_pass "no comma-decimal locale installed; covered by the normalize tests"
    return
  fi
  # Fixed EPOCHREALTIME-style values with a comma radix, as bash prints them under $loc: no clock, no sleep.
  out="$(NO_COLOR=1 LC_ALL="$loc" bash -c 'source "$1"; bash_test_time_line a "1731000000,000000" "1731000001,000000"' _ "$PROJECT_ROOT/test/lib/bash_test.sh")"
  assert_eq "[TIME] a: 1.00s" "$out" "a one second test reports 1.00s with a dot under $loc"
}

test_parallel_runner_prints_in_order_and_fails_on_a_failing_test() {
  local out rc=0 script
  script='source "$1"
test_a() { sleep 0.2; test_pass "a ran"; }
test_b() { test_pass "b ran"; }
test_c() { assert_eq 1 2 "c fails"; }
BASH_TEST_JOBS=3 run_tests test_a test_b test_c'
  out="$(NO_COLOR=1 bash -c "$script" _ "$PROJECT_ROOT/test/lib/bash_test.sh" 2>&1)" || rc=$?
  assert_status 1 "$rc" "a failing parallel test fails the suite"
  assert_eq "[PASS] a ran
[PASS] b ran
[FAIL] c fails
Expected: [1]
Actual:   [2]" "$(grep -v '^\[RUN\]' <<<"$out")" "output is in test order and shows the failure"
  script='source "$1"
test_a() { test_pass "a ran"; }
test_b() { test_pass "b ran"; }
BASH_TEST_JOBS=2 run_tests test_a test_b'
  out="$(NO_COLOR=1 bash -c "$script" _ "$PROJECT_ROOT/test/lib/bash_test.sh" 2>&1)"
  assert_contains "$out" "SUMMARY: 2 test(s), 2 assertion(s)" "passing parallel tests are counted"
}

run_tests "$@"
