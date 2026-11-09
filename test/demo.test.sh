#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
DEMO="$PROJECT_ROOT/bin/demo"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; TEST_TMP=""; return 0; }
trap 'cleanup_test_tmp' EXIT

# Fresh temp dir for every test; the demo state file lives there, never in the real temp dir.
setup_tmp() {
  cleanup_test_tmp
  TEST_TMP="$(mktemp -d)"
  export BBAPPROVE_DEMO_STATE="$TEST_TMP/state" STUB_LOG="$TEST_TMP/calls.log"
  : >"$STUB_LOG"
}

test_usage_and_unknown_commands() {
  setup_tmp
  local out rc
  capture_command out rc "$DEMO"
  assert_status 2 "$rc" "no command exits 2"
  assert_contains "$out" "bin/demo record" "usage lists record"
  capture_command out rc "$DEMO" --help
  assert_status 0 "$rc" "--help exits 0"
  capture_command out rc "$DEMO" bogus
  assert_status 2 "$rc" "unknown command exits 2"
  capture_command out rc "$DEMO" list extra
  assert_status 2 "$rc" "list takes no arguments"
}

test_record_without_vhs_hints_and_exits_127() {
  setup_tmp
  local out rc
  capture_command out rc env PATH="$TEST_TMP/empty:/usr/bin:/bin" "$DEMO" record
  assert_status 127 "$rc" "record exits 127 when vhs is missing"
  assert_contains "$out" "brew install vhs" "record prints the install hint"
}

test_shots_without_freeze_hints_and_exits_127() {
  setup_tmp
  local out rc
  capture_command out rc env PATH="$TEST_TMP/empty:/usr/bin:/bin" "$DEMO" shots
  assert_status 127 "$rc" "shots exits 127 when freeze is missing"
  assert_contains "$out" "charmbracelet/tap/freeze" "shots prints the install hint"
}

test_record_runs_vhs_on_the_tape_and_resets_state() {
  setup_tmp
  mkdir -p "$TEST_TMP/bin"
  use_stubs "$TEST_TMP/bin" recorder=vhs
  printf 'approved 101\n' >"$BBAPPROVE_DEMO_STATE"
  local out rc
  capture_command out rc env PATH="$TEST_TMP/bin:/usr/bin:/bin" "$DEMO" record
  assert_status 0 "$rc" "record exits with vhs's status"
  assert_contains "$(cat "$STUB_LOG")" "vhs demo/demo.tape" "record runs vhs on demo/demo.tape"
  if [[ -e "$BBAPPROVE_DEMO_STATE" ]]; then test_fail "record starts from the fresh demo data"; fi
  test_pass "record starts from the fresh demo data"
}

test_shots_runs_freeze_for_both_screens() {
  setup_tmp
  mkdir -p "$TEST_TMP/bin"
  use_stubs "$TEST_TMP/bin" recorder=freeze
  local out rc
  capture_command out rc env PATH="$TEST_TMP/bin:/usr/bin:/bin" "$DEMO" shots
  assert_status 0 "$rc" "shots exits with freeze's status"
  assert_contains "$(cat "$STUB_LOG")" "freeze --execute bin/demo list -o demo/list.svg" "shots writes the list screen"
  assert_contains "$(cat "$STUB_LOG")" "print-config.svg" "shots writes the config screen"
}

run_tests "$@"
