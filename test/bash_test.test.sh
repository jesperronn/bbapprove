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

run_tests "$@"
