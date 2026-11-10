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
  assert_contains "$out" "Subcommands:" "no command prints the help"
  assert_contains "$out" "regenerate" "help lists regenerate"
  capture_command out rc "$DEMO" --help
  assert_status 0 "$rc" "--help exits 0"
  assert_contains "$out" "reset" "--help lists reset"
  capture_command out rc "$DEMO" -h
  assert_status 0 "$rc" "-h exits 0"
  capture_command out rc "$DEMO" bogus
  assert_status 2 "$rc" "unknown subcommand exits 2"
  assert_contains "$out" "unknown subcommand: bogus" "unknown subcommand is named"
  assert_contains "$out" "run list record shots regenerate reset help" "unknown subcommand lists the valid ones"
}

test_help_is_plain_without_a_terminal_and_coloured_when_forced() {
  setup_tmp
  local out rc
  capture_command out rc env -u FORCE_COLOR "$DEMO" --help
  assert_status 0 "$rc" "help exits 0 without a terminal"
  assert_not_contains "$out" $'\033[' "help is plain when stdout is not a terminal"
  capture_command out rc env FORCE_COLOR=1 "$DEMO" --help
  assert_contains "$out" $'\033[1;35mrecord' "subcommand names use the cmd colour when forced"
  capture_command out rc env NO_COLOR=1 FORCE_COLOR=1 "$DEMO" --help
  assert_not_contains "$out" $'\033[' "NO_COLOR wins over FORCE_COLOR"
}

test_each_subcommand_has_its_own_help() {
  setup_tmp
  local out rc sub
  for sub in run list record shots regenerate reset; do
    capture_command out rc "$DEMO" help "$sub"
    assert_status 0 "$rc" "bin/demo help $sub exits 0"
    assert_contains "$out" "Usage: bin/demo $sub" "bin/demo help $sub names its usage"
  done
  capture_command out rc "$DEMO" record --help
  assert_status 0 "$rc" "record --help exits 0"
  assert_contains "$out" "Needs:" "record --help says what it needs"
  capture_command out rc "$DEMO" list --help
  assert_status 0 "$rc" "list --help exits 0 and does not run bbapprove"
  assert_contains "$out" "Usage: bin/demo list" "list --help prints its own page"
}

test_reset_removes_the_state_file_and_is_idempotent() {
  setup_tmp
  local out rc
  printf 'approved 102\n' >"$BBAPPROVE_DEMO_STATE"
  capture_command out rc "$DEMO" reset
  assert_status 0 "$rc" "reset exits 0"
  assert_contains "$out" "removed $BBAPPROVE_DEMO_STATE" "reset names the file it removed"
  assert_eq "no" "$([[ -e "$BBAPPROVE_DEMO_STATE" ]] && echo yes || echo no)" "reset deletes the state file"
  capture_command out rc "$DEMO" reset
  assert_status 0 "$rc" "reset exits 0 when there is nothing to remove"
  assert_contains "$out" "nothing to remove" "reset says so when there is nothing to remove"
}

test_regenerate_checks_tools_before_recording() {
  setup_tmp
  local out rc
  make_tool_dir "$TEST_TMP/basic"
  capture_command out rc env PATH="$TEST_TMP/basic" "$DEMO" regenerate
  assert_status 127 "$rc" "regenerate exits 127 when tmux is missing"
  assert_contains "$out" "tmux is not installed (needed by regenerate)" "regenerate names the missing tool"
}

run_tests "$@"
