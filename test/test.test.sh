#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
RUNTESTS="$PROJECT_ROOT/bin/runtests"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

ORIG_PATH="$PATH"
TEST_TMP=""
PROJ=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; TEST_TMP=""; PATH="$ORIG_PATH"; return 0; }
trap 'cleanup_test_tmp' EXIT

RUNTESTS_OUT=""
RUNTESTS_STATUS=0

# Fake project with bin/runtests copied in, three fake bash suites that log their
# argv, and a stub go on PATH that logs its argv. Nothing here touches the real suites.
setup_fixture() {
  TEST_TMP="$(mktemp -d)"
  PROJ="$TEST_TMP/proj"
  mkdir -p "$PROJ/bin" "$PROJ/test" "$TEST_TMP/stubs"
  cp "$RUNTESTS" "$PROJ/bin/runtests"

  for suite in a b c; do
    cat >"$PROJ/test/$suite.test.sh" <<'SUITE'
#!/usr/bin/env bash
echo "suite $(basename "$0")${*:+ $*}" >>"$RUNTESTS_TEST_LOG"
if [[ "$(basename "$0")" == "${FAKE_FAIL_SUITE:-}" ]]; then
  exit 1
fi
SUITE
  done

  cat >"$TEST_TMP/stubs/go" <<'STUB'
#!/usr/bin/env bash
echo "go $*" >>"$RUNTESTS_TEST_LOG"
exit "${STUB_GO_RC:-0}"
STUB
  chmod +x "$TEST_TMP/stubs/go"

  export RUNTESTS_TEST_LOG="$TEST_TMP/calls.log"
  : >"$RUNTESTS_TEST_LOG"
  export PATH="$TEST_TMP/stubs:$ORIG_PATH"
  unset STUB_GO_RC FAKE_FAIL_SUITE
}

# run_runtests ARGS...: run bin/runtests from the fixture project; sets RUNTESTS_OUT and RUNTESTS_STATUS.
run_runtests() {
  RUNTESTS_STATUS=0
  RUNTESTS_OUT="$(cd "$PROJ" && bash bin/runtests "$@" 2>&1)" || RUNTESTS_STATUS=$?
}

test_help_exits_zero() {
  setup_fixture
  run_runtests --help
  assert_status 0 "$RUNTESTS_STATUS" "--help exits 0"
  assert_contains "$RUNTESTS_OUT" "Usage: bin/runtests" "--help prints usage"
  cleanup_test_tmp
}

test_default_runs_go_then_each_suite() {
  setup_fixture
  run_runtests
  local -a lines=()
  mapfile -t lines <"$RUNTESTS_TEST_LOG"
  assert_status 0 "$RUNTESTS_STATUS" "default run exits 0 when everything passes"
  assert_eq "${#lines[@]}" "4" "default run: go plus three suites"
  assert_eq "${lines[0]}" "go test ./... -count=1 -race" "go test runs first with -count=1 -race"
  assert_eq "${lines[1]}" "suite a.test.sh" "suite a runs second"
  assert_eq "${lines[2]}" "suite b.test.sh" "suite b runs third"
  assert_eq "${lines[3]}" "suite c.test.sh" "suite c runs fourth"
  assert_contains "$RUNTESTS_OUT" "TEST SUMMARY: all passed" "summary reports all passed"
  cleanup_test_tmp
}

test_failing_suite_does_not_stop_others() {
  setup_fixture
  export FAKE_FAIL_SUITE=b.test.sh
  run_runtests
  assert_status 1 "$RUNTESTS_STATUS" "a failing suite makes exit 1"
  assert_contains "$(<"$RUNTESTS_TEST_LOG")" "suite c.test.sh" "suite after the failing one still runs"
  assert_contains "$RUNTESTS_OUT" "TEST SUMMARY: 1 failed: b.test.sh" "summary names the failed suite"
  cleanup_test_tmp
}

test_failing_go_does_not_stop_shell_suites() {
  setup_fixture
  export STUB_GO_RC=1
  run_runtests
  assert_status 1 "$RUNTESTS_STATUS" "failing go test makes exit 1"
  assert_contains "$(<"$RUNTESTS_TEST_LOG")" "suite a.test.sh" "shell suites still run after go fails"
  assert_contains "$RUNTESTS_OUT" "TEST SUMMARY: 1 failed: go:./..." "summary names the failed go run"
  cleanup_test_tmp
}

test_no_go_skips_go() {
  setup_fixture
  run_runtests --no-go
  local log
  log="$(<"$RUNTESTS_TEST_LOG")"
  assert_status 0 "$RUNTESTS_STATUS" "--no-go exits 0"
  assert_not_contains "$log" "go test" "--no-go runs no go test"
  assert_contains "$log" "suite c.test.sh" "--no-go still runs shell suites"
  cleanup_test_tmp
}

test_no_shell_skips_shell_suites() {
  setup_fixture
  run_runtests --no-shell
  assert_status 0 "$RUNTESTS_STATUS" "--no-shell exits 0"
  assert_eq "$(<"$RUNTESTS_TEST_LOG")" "go test ./... -count=1 -race" "--no-shell runs only go test"
  cleanup_test_tmp
}

test_explicit_suite_runs_only_that_file() {
  setup_fixture
  run_runtests test/b.test.sh
  assert_status 0 "$RUNTESTS_STATUS" "explicit suite exits 0"
  assert_eq "$(<"$RUNTESTS_TEST_LOG")" "suite b.test.sh" "explicit *.test.sh runs only that suite"
  assert_contains "$RUNTESTS_OUT" "TEST SUMMARY: all passed" "explicit suite reports all passed"
  cleanup_test_tmp
}

test_explicit_go_pattern_runs_only_that_package() {
  setup_fixture
  run_runtests ./internal/tui
  assert_status 0 "$RUNTESTS_STATUS" "explicit package pattern exits 0"
  assert_eq "$(<"$RUNTESTS_TEST_LOG")" "go test ./internal/tui -count=1 -race" "package pattern runs only go test on that package"
  cleanup_test_tmp
}

test_verbose_is_passed_through() {
  setup_fixture
  run_runtests --verbose
  local log
  log="$(<"$RUNTESTS_TEST_LOG")"
  assert_contains "$log" "go test ./... -count=1 -race -v" "--verbose passes -v to go test"
  assert_contains "$log" "suite a.test.sh --verbose" "--verbose passes --verbose to shell suites"
  cleanup_test_tmp
}

test_nothing_to_run_exits_2() {
  setup_fixture
  run_runtests --no-go --no-shell
  assert_status 2 "$RUNTESTS_STATUS" "--no-go --no-shell with no targets exits 2"
  assert_contains "$RUNTESTS_OUT" "runtests: nothing to run" "nothing-to-run is reported"
  cleanup_test_tmp
}

test_missing_suite_exits_2() {
  setup_fixture
  run_runtests test/nope.test.sh
  assert_status 2 "$RUNTESTS_STATUS" "missing suite file exits 2"
  assert_contains "$RUNTESTS_OUT" "runtests: no such suite: test/nope.test.sh" "missing suite is reported"
  cleanup_test_tmp
}

test_unknown_option_exits_2() {
  setup_fixture
  run_runtests --bogus
  assert_status 2 "$RUNTESTS_STATUS" "unknown option exits 2"
  assert_contains "$RUNTESTS_OUT" "runtests: unknown option: --bogus" "unknown option is reported"
  cleanup_test_tmp
}

run_tests "$@"
