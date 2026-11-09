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

test_time_lines_are_sane_under_a_comma_locale() {
  local loc out
  loc="$(locale -a 2>/dev/null | grep -i -m1 -E '^(da_DK|de_DE|fr_FR)' || true)"
  if [[ -z "$loc" ]]; then
    test_pass "no comma-decimal locale installed; covered by the normalize tests"
    return
  fi
  out="$(BASH_TEST_VERBOSE=1 LC_ALL="$loc" bash -c 'source "$1"; a() { sleep 1; }; run_test_function a' _ "$PROJECT_ROOT/test/lib/bash_test.sh" 2>&1)"
  assert_contains "$out" "a: 1.0" "a one second test reports about 1s under $loc"
}

run_tests "$@"
