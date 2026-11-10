#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

source "$PROJECT_ROOT/test/lib/bash_test.sh"
source "$PROJECT_ROOT/bin/lib/style.sh"

TEST_TMP=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
trap 'cleanup_test_tmp' EXIT

ESC=$'\033['
SAMPLE=$'Usage: bin/x [--flag] [-h]\n\n  next   a pre-release, BBAPPROVE_X\nExit: 0 ok'

# styled [ENV=VALUE...]: SAMPLE through style_help in a pipe, with only those variables set.
styled() {
  printf '%s\n' "$SAMPLE" | env -u NO_COLOR -u FORCE_COLOR "$@" bash -c 'source "$1"; style_help' _ "$PROJECT_ROOT/bin/lib/style.sh"
}

test_style_help_is_plain_without_colour() {
  assert_eq "$(styled)" "$SAMPLE" "not a tty: passthrough"
  assert_eq "$(styled NO_COLOR=1)" "$SAMPLE" "NO_COLOR: passthrough"
  assert_eq "$(styled NO_COLOR=1 FORCE_COLOR=1)" "$SAMPLE" "NO_COLOR wins over FORCE_COLOR"
}

test_style_help_paints_with_force_color() {
  local out
  out="$(styled FORCE_COLOR=1)"
  assert_contains "$out" $'\033[1;36mUsage:\033[0m' "Usage: is a heading"
  assert_contains "$out" $'\033[32m--flag\033[0m' "--flag is a flag"
  assert_contains "$out" $'\033[32m-h\033[0m' "-h is a flag"
  assert_contains "$out" $'\033[1;35mnext\033[0m' "a row name is a command"
  assert_contains "$out" $'\033[33mBBAPPROVE_X\033[0m' "an env name is painted"
  assert_contains "$out" "a pre-release," "pre-release is not painted"
}

test_paint_matches_the_palette() {
  assert_eq "$(FORCE_COLOR=1 paint ok 1 hi)" $'\033[32mhi\033[0m' "ok is 32"
  assert_eq "$(NO_COLOR=1 FORCE_COLOR=1 paint fail 1 hi)" "hi" "NO_COLOR disables paint"
}

test_every_script_colours_its_help() {
  local f name plain forced off
  local -a names=()
  TEST_TMP="$(mktemp -d)"
  # The three runs of every script go in parallel: each costs about 50 ms.
  for f in "$PROJECT_ROOT"/bin/*; do
    [[ -f "$f" && "$(head -c 2 "$f")" == "#!" ]] || continue
    name="$(basename "$f")"
    [[ "$name" == bbapprove ]] && continue # the launcher has no help of its own
    [[ "$name" == changelog ]] && continue # a Go program; its test is in cmd/changelog
    names+=("$name")
    env -u NO_COLOR -u FORCE_COLOR "$f" --help >"$TEST_TMP/$name.plain" 2>&1 &
    env -u NO_COLOR FORCE_COLOR=1 "$f" --help >"$TEST_TMP/$name.forced" 2>&1 &
    env NO_COLOR=1 FORCE_COLOR=1 "$f" --help >"$TEST_TMP/$name.off" 2>&1 &
  done
  wait
  for name in "${names[@]}"; do
    plain="$(<"$TEST_TMP/$name.plain")"
    forced="$(<"$TEST_TMP/$name.forced")"
    off="$(<"$TEST_TMP/$name.off")"
    assert_contains "$forced" "$ESC" "FORCE_COLOR=1 bin/$name --help is coloured"
    assert_not_contains "$off" "$ESC" "NO_COLOR=1 bin/$name --help is plain"
    assert_eq "$off" "$plain" "bin/$name --help is the same plain when piped"
  done
}

run_tests "$@"
