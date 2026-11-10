#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TESTSH="$PROJECT_ROOT/bin/test"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

ORIG_PATH="$PATH"
TEST_TMP=""
PROJ=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  PATH="$ORIG_PATH"
  return 0
}
trap 'cleanup_test_tmp' EXIT

TESTSH_OUT=""
TESTSH_STATUS=0

# Fake project with bin/test copied in, three fake bash suites that log their
# argv, and a stub go on PATH that logs its argv. Nothing here touches the real suites.
setup_fixture() {
  TEST_TMP="$(mktemp -d)"
  PROJ="$TEST_TMP/proj"
  mkdir -p "$PROJ/bin" "$PROJ/test" "$TEST_TMP/stubs"
  link_script "$TESTSH" "$PROJ/bin/test"
  add_style_lib "$PROJ/bin"

  # Fixed fixtures, symlinked: three fake suites (b sleeps for FAKE_SLOW_B, FAKE_FAIL_SUITE fails) and a go stub
  # that logs its argv and prints canned `go test -json` lines under STUB_GO_JSON=timings.
  use_stubs "$PROJ/test" fake-suite=a.test.sh fake-suite=b.test.sh fake-suite=c.test.sh
  use_stubs "$TEST_TMP/stubs" go-stub=go
  export STUB_LOG="$TEST_TMP/calls.log" STUB_GO_JSON=timings

  export TESTSH_TEST_LOG="$STUB_LOG"
  : >"$TESTSH_TEST_LOG"
  export PATH="$TEST_TMP/stubs:$ORIG_PATH"
  unset STUB_GO_RC FAKE_FAIL_SUITE FAKE_SLOW_B STUB_GO_LOG_MATCH STUB_GO_LOG_PREFIX STUB_GO_FAIL STUB_GO_WRITE_BINARY TEST_TIMINGS_TOP TEST_SLOW_SUITE_SECONDS TEST_SLOW_TEST_SECONDS FAKE_TEST_SECONDS
}

# run_testsh ARGS...: run bin/test from the fixture project; sets TESTSH_OUT and TESTSH_STATUS.
run_testsh() {
  TESTSH_STATUS=0
  TESTSH_OUT="$(cd "$PROJ" && bash bin/test "$@" 2>&1)" || TESTSH_STATUS=$?
}

test_help_exits_zero() {
  setup_fixture
  run_testsh --help
  assert_status 0 "$TESTSH_STATUS" "--help exits 0"
  assert_contains "$TESTSH_OUT" "Usage: bin/test" "--help prints usage"
  cleanup_test_tmp
}

test_default_runs_go_then_each_suite() {
  setup_fixture
  run_testsh
  local -a lines=()
  mapfile -t lines <"$TESTSH_TEST_LOG"
  assert_status 0 "$TESTSH_STATUS" "default run exits 0 when everything passes"
  assert_eq "${#lines[@]}" "4" "default run: go plus three suites"
  assert_eq "${lines[0]}" "go test ./... -count=1 -race" "go test runs first with -count=1 -race"
  assert_eq "${lines[1]}" "suite a.test.sh" "suite a runs second"
  assert_eq "${lines[2]}" "suite b.test.sh" "suite b runs third"
  assert_eq "${lines[3]}" "suite c.test.sh" "suite c runs fourth"
  assert_contains "$TESTSH_OUT" "TEST SUMMARY: all passed" "summary reports all passed"
  cleanup_test_tmp
}

test_failing_suite_does_not_stop_others() {
  setup_fixture
  export FAKE_FAIL_SUITE=b.test.sh
  run_testsh
  assert_status 1 "$TESTSH_STATUS" "a failing suite makes exit 1"
  assert_contains "$(<"$TESTSH_TEST_LOG")" "suite c.test.sh" "suite after the failing one still runs"
  assert_contains "$TESTSH_OUT" "TEST SUMMARY: 1 failed: b.test.sh" "summary names the failed suite"
  cleanup_test_tmp
}

test_failing_go_does_not_stop_shell_suites() {
  setup_fixture
  export STUB_GO_RC=1
  run_testsh
  assert_status 1 "$TESTSH_STATUS" "failing go test makes exit 1"
  assert_contains "$(<"$TESTSH_TEST_LOG")" "suite a.test.sh" "shell suites still run after go fails"
  assert_contains "$TESTSH_OUT" "TEST SUMMARY: 1 failed: go:./..." "summary names the failed go run"
  cleanup_test_tmp
}

test_no_go_skips_go() {
  setup_fixture
  run_testsh --no-go
  local log
  log="$(<"$TESTSH_TEST_LOG")"
  assert_status 0 "$TESTSH_STATUS" "--no-go exits 0"
  assert_not_contains "$log" "go test" "--no-go runs no go test"
  assert_contains "$log" "suite c.test.sh" "--no-go still runs shell suites"
  cleanup_test_tmp
}

test_no_shell_skips_shell_suites() {
  setup_fixture
  run_testsh --no-shell
  assert_status 0 "$TESTSH_STATUS" "--no-shell exits 0"
  assert_eq "$(<"$TESTSH_TEST_LOG")" "go test ./... -count=1 -race" "--no-shell runs only go test"
  cleanup_test_tmp
}

test_explicit_suite_runs_only_that_file() {
  setup_fixture
  run_testsh test/b.test.sh
  assert_status 0 "$TESTSH_STATUS" "explicit suite exits 0"
  assert_eq "$(<"$TESTSH_TEST_LOG")" "suite b.test.sh" "explicit *.test.sh runs only that suite"
  assert_contains "$TESTSH_OUT" "TEST SUMMARY: all passed" "explicit suite reports all passed"
  cleanup_test_tmp
}

test_explicit_go_pattern_runs_only_that_package() {
  setup_fixture
  run_testsh ./internal/tui
  assert_status 0 "$TESTSH_STATUS" "explicit package pattern exits 0"
  assert_eq "$(<"$TESTSH_TEST_LOG")" "go test ./internal/tui -count=1 -race" "package pattern runs only go test on that package"
  cleanup_test_tmp
}

test_verbose_is_passed_through() {
  setup_fixture
  run_testsh --verbose
  local log
  log="$(<"$TESTSH_TEST_LOG")"
  assert_contains "$log" "go test ./... -count=1 -race -v" "--verbose passes -v to go test"
  assert_contains "$log" "suite a.test.sh --verbose" "--verbose passes --verbose to shell suites"
  cleanup_test_tmp
}

test_nothing_to_run_exits_2() {
  setup_fixture
  run_testsh --no-go --no-shell
  assert_status 2 "$TESTSH_STATUS" "--no-go --no-shell with no targets exits 2"
  assert_contains "$TESTSH_OUT" "test: nothing to run" "nothing-to-run is reported"
  cleanup_test_tmp
}

test_missing_suite_exits_2() {
  setup_fixture
  run_testsh test/nope.test.sh
  assert_status 2 "$TESTSH_STATUS" "missing suite file exits 2"
  assert_contains "$TESTSH_OUT" "test: no such suite: test/nope.test.sh" "missing suite is reported"
  cleanup_test_tmp
}

test_unknown_option_exits_2() {
  setup_fixture
  run_testsh --bogus
  assert_status 2 "$TESTSH_STATUS" "unknown option exits 2"
  assert_contains "$TESTSH_OUT" "test: unknown option: --bogus" "unknown option is reported"
  cleanup_test_tmp
}

line_no() { grep -n -m1 -F -- "$2" <<<"$1" | cut -d: -f1 || true; }

test_timings_absent_without_flag() {
  setup_fixture
  run_testsh
  assert_not_contains "$TESTSH_OUT" "TIMINGS" "no report without --timings"
  assert_not_contains "$TESTSH_OUT" "Total wall time" "no total without --timings"
  cleanup_test_tmp
}

test_timings_report_lists_suites_packages_tests() {
  setup_fixture
  export NO_COLOR=1
  run_testsh --timings
  assert_status 0 "$TESTSH_STATUS" "--timings keeps exit 0"
  assert_contains "$(<"$TESTSH_TEST_LOG")" "go test ./... -count=1 -race -json" "--timings runs go test with -json"
  assert_contains "$TESTSH_OUT" "==> TIMINGS" "report header"
  assert_contains "$TESTSH_OUT" "Shell suites, slowest first" "shell section"
  assert_contains "$TESTSH_OUT" "Go packages, slowest first" "package section"
  assert_contains "$TESTSH_OUT" "Slowest Go tests (top 10)" "tests section"
  assert_contains "$TESTSH_OUT" "Total wall time:" "total wall time"
  assert_contains "$TESTSH_OUT" "ok  	x/slow	1.8s" "package output lines are still printed"
  assert_not_contains "$TESTSH_OUT" "hidden" "passing test output stays hidden without --verbose"
  assert_not_contains "$TESTSH_OUT" '{"Action"' "raw JSON is not shown"
  assert_contains "$TESTSH_OUT" "TEST SUMMARY: all passed" "summary still printed"
  unset NO_COLOR
  cleanup_test_tmp
}

test_timings_orders_slowest_first() {
  setup_fixture
  export FAKE_SLOW_B=0.3
  run_testsh --timings
  local out="$TESTSH_OUT"
  assert_contains "$out" "  b.test.sh" "slow suite listed"
  # In the report the slow suite precedes the fast ones.
  local rep="${out#*==> TIMINGS}"
  local lb la
  lb="$(line_no "$rep" "  b.test.sh")"
  la="$(line_no "$rep" "  a.test.sh")"
  [[ -n "$lb" && -n "$la" && "$lb" -lt "$la" ]] || test_fail "suite b (slow) listed before a" "$rep"
  test_pass "suites sorted slowest first"
  lb="$(line_no "$rep" "x/slow")"
  la="$(line_no "$rep" "x/fast")"
  [[ -n "$lb" && -n "$la" && "$lb" -lt "$la" ]] || test_fail "package x/slow listed before x/fast" "$rep"
  test_pass "packages sorted slowest first"
  lb="$(line_no "$rep" "x/slow.TestB")"
  la="$(line_no "$rep" "x/slow.TestA")"
  [[ -n "$lb" && -n "$la" && "$lb" -lt "$la" ]] || test_fail "TestB (1.5s) listed before TestA (0.25s)" "$rep"
  test_pass "tests sorted slowest first"
  cleanup_test_tmp
}

test_timings_limits_slowest_tests() {
  setup_fixture
  export TEST_TIMINGS_TOP=1
  run_testsh --timings
  assert_contains "$TESTSH_OUT" "x/slow.TestB" "the slowest test is shown"
  assert_not_contains "$TESTSH_OUT" "x/fast.TestC" "tests beyond the limit are cut"
  cleanup_test_tmp
}

test_timings_with_no_go() {
  setup_fixture
  run_testsh --timings --no-go
  assert_status 0 "$TESTSH_STATUS" "--timings --no-go exits 0"
  assert_contains "$TESTSH_OUT" "Shell suites, slowest first" "shell section still shown"
  assert_not_contains "$TESTSH_OUT" "Go packages" "no go section without go"
  assert_contains "$TESTSH_OUT" "Total wall time:" "total shown"
  cleanup_test_tmp
}

test_timings_with_no_shell() {
  setup_fixture
  run_testsh --timings --no-shell
  assert_status 0 "$TESTSH_STATUS" "--timings --no-shell exits 0"
  assert_not_contains "$TESTSH_OUT" "Shell suites" "no shell section without shell suites"
  assert_contains "$TESTSH_OUT" "Go packages, slowest first" "go section shown"
  cleanup_test_tmp
}

test_timings_with_target_and_failure_exit() {
  setup_fixture
  export FAKE_FAIL_SUITE=b.test.sh
  run_testsh --timings test/b.test.sh
  assert_status 1 "$TESTSH_STATUS" "--timings keeps the failing exit code"
  assert_contains "$TESTSH_OUT" "b.test.sh" "target suite timed"
  assert_not_contains "$TESTSH_OUT" "a.test.sh" "other suites not run"
  cleanup_test_tmp
}

test_timings_lists_shell_tests_and_budgets() {
  setup_fixture
  export TEST_SLOW_SUITE_SECONDS=1000 TEST_SLOW_TEST_SECONDS=1000
  run_testsh --timings
  unset TEST_SLOW_SUITE_SECONDS TEST_SLOW_TEST_SECONDS
  assert_status 0 "$TESTSH_STATUS" "--timings exits 0"
  assert_contains "$TESTSH_OUT" "Slowest shell tests (top 10)" "the shell tests section is shown"
  assert_contains "$TESTSH_OUT" "a.test.sh:test_fake" "shell tests are shown as suite:test"
  assert_contains "$TESTSH_OUT" "Over budget: none" "huge budgets: nothing is over"
  cleanup_test_tmp
}

test_over_budget_flags_and_keeps_exit_code() {
  setup_fixture
  export NO_COLOR=1
  export TEST_SLOW_SUITE_SECONDS=0 TEST_SLOW_TEST_SECONDS=0.4
  run_testsh --timings
  assert_status 0 "$TESTSH_STATUS" "being over budget keeps exit 0"
  assert_contains "$TESTSH_OUT" "go-package  x/slow" "slow package listed (1.8s > 0s)"
  assert_contains "$TESTSH_OUT" "shell-test  a.test.sh:test_fake" "shell test over 0.4s listed"
  assert_contains "$TESTSH_OUT" "go-test     x/slow.TestB" "go test over 0.4s listed"
  assert_not_contains "$TESTSH_OUT" "go-test     x/slow.TestA" "go test under 0.4s not listed"
  unset NO_COLOR TEST_SLOW_SUITE_SECONDS TEST_SLOW_TEST_SECONDS
  cleanup_test_tmp
}

run_tests "$@"
