#!/usr/bin/env bash
# shellcheck shell=bash
# bash_test — helper library for bin/*.test.sh: colored output, test_pass/test_fail and assert_* functions.

set -euo pipefail

BASH_TEST_VERBOSE="${BASH_TEST_VERBOSE:-0}"
BASH_TEST_ASSERTIONS=0
BASH_TEST_CASES=0

if [[ -n "${NO_COLOR:-}" ]]; then
  BASH_TEST_C_RESET=""
  BASH_TEST_C_BOLD=""
  BASH_TEST_C_GREEN=""
  BASH_TEST_C_RED=""
  BASH_TEST_C_CYAN=""
else
  BASH_TEST_C_RESET=$'\033[0m'
  BASH_TEST_C_BOLD=$'\033[1m'
  BASH_TEST_C_GREEN=$'\033[32m'
  BASH_TEST_C_RED=$'\033[31m'
  BASH_TEST_C_CYAN=$'\033[36m'
fi

bash_test_color_print() {
  local color="$1"
  shift
  printf '%b%s%b\n' "$color" "$*" "$BASH_TEST_C_RESET"
}

bash_test_status_print() {
  local color="$1"
  local marker="$2"
  shift 2

  printf '%b%s%b' "$color" "$marker" "$BASH_TEST_C_RESET"
  if [[ $# -gt 0 ]]; then
    printf ' %s' "$*"
  fi
  printf '\n'
}

test_run() {
  if [[ "${BASH_TEST_VERBOSE}" == "1" ]]; then
    bash_test_color_print "${BASH_TEST_C_BOLD}${BASH_TEST_C_CYAN}" "[RUN] $*"
  fi
}

test_pass() {
  BASH_TEST_ASSERTIONS=$((BASH_TEST_ASSERTIONS + 1))
  bash_test_status_print "$BASH_TEST_C_GREEN" "[PASS]" "$*"
}

test_fail() {
  local label="$1"
  shift || true
  bash_test_status_print "$BASH_TEST_C_RED" "[FAIL]" "$label" >&2
  if [[ $# -gt 0 ]]; then
    printf '%s\n' "$@" >&2
  fi
  exit 1
}

assert_eq() {
  local expected="$1"
  local actual="$2"
  local label="$3"

  if [[ "$expected" != "$actual" ]]; then
    test_fail "$label" "Expected: [$expected]" "Actual:   [$actual]"
  fi

  test_pass "$label"
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"

  if [[ "$haystack" != *"$needle"* ]]; then
    test_fail "$label" "Expected substring: [$needle]" "Actual output:      [$haystack]"
  fi

  test_pass "$label"
}

assert_not_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"

  if [[ "$haystack" == *"$needle"* ]]; then
    test_fail "$label" "Unexpected substring: [$needle]" "Actual output:       [$haystack]"
  fi

  test_pass "$label"
}

assert_status() {
  local expected="$1"
  local actual="$2"
  local label="$3"

  if [[ "$expected" != "$actual" ]]; then
    test_fail "$label" "Expected status: [$expected]" "Actual status:   [$actual]"
  fi

  test_pass "$label"
}

assert_not_status() {
  local unexpected="$1"
  local actual="$2"
  local label="$3"

  if [[ "$unexpected" == "$actual" ]]; then
    test_fail "$label" "Unexpected status: [$actual]"
  fi

  test_pass "$label"
}

capture_command() {
  local output_var="$1"
  local status_var="$2"
  shift 2

  local output_file
  local captured_output=""
  local captured_status=0

  output_file="$(mktemp)"

  set +e
  "$@" >"$output_file" 2>&1
  captured_status=$?
  set -e

  captured_output="$(cat "$output_file")"
  rm -f "$output_file"

  printf -v "$output_var" '%s' "$captured_output"
  printf -v "$status_var" '%s' "$captured_status"
}

# Fixed stub fixtures live in test/fixtures (see docs/development.md). Never write a stub script per test:
# a freshly written executable costs 45-200 ms on its first run on macOS; a symlink to a fixture costs nothing.
BASH_TEST_FIXTURES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../fixtures" && pwd -P)"

# fixture_path NAME: absolute path of test/fixtures/NAME.
fixture_path() {
  printf '%s/%s\n' "$BASH_TEST_FIXTURES_DIR" "$1"
}

# use_stubs DIR SPEC...: create DIR holding one symlink per SPEC, each NAME (linked as NAME) or NAME=AS
# (fixture NAME linked as AS, e.g. recorder=shellcheck). Put DIR on PATH, or use it as bin/ of a fake repo.
use_stubs() {
  local dir="$1" spec name as
  shift
  mkdir -p "$dir"
  for spec in "$@"; do
    name="${spec%%=*}"
    as="${spec#*=}"
    ln -sf "$(fixture_path "$name")" "$dir/$as"
  done
}

# link_script SRC DEST: hardlink the script under test into a fake repo (a copy is a fresh, cold executable on
# macOS; a hardlink shares the already-warm inode). Falls back to cp across filesystems. Never write to DEST.
link_script() {
  ln -f "$1" "$2" 2>/dev/null || cp "$1" "$2"
}

# bash_test_normalize_time T: T (an EPOCHREALTIME value) with a '.' radix. Under comma-decimal locales bash
# prints "1731,123456", which awk reads as 1731, so every [TIME] line would be wrong.
bash_test_normalize_time() {
  local t="$1"
  printf '%s.%s\n' "${t%[.,]*}" "${t#*[.,]}"
}

bash_test_now() {
  bash_test_normalize_time "$EPOCHREALTIME"
}

run_test_function() {
  local test_name="$1"
  local start_time="" end_time="" elapsed=""

  test_run "$test_name"
  start_time="$(bash_test_now)"
  "$test_name"
  end_time="$(bash_test_now)"
  BASH_TEST_CASES=$((BASH_TEST_CASES + 1))

  if [[ "${BASH_TEST_VERBOSE}" == "1" ]]; then
    elapsed="$(LC_ALL=C awk -v s="$start_time" -v e="$end_time" 'BEGIN { printf "%.2f", e - s }')"
    bash_test_color_print "${BASH_TEST_C_CYAN}" "[TIME] $test_name: ${elapsed}s"
  fi
}

run_tests() {
  local -a requested_tests=()
  local -a test_names=()
  local test_name=""
  local arg=""
  local suite_start="" suite_end="" suite_elapsed=""

  for arg in "$@"; do
    case "$arg" in
      --verbose|-v)
        BASH_TEST_VERBOSE=1
        ;;
      *)
        requested_tests+=("$arg")
        ;;
    esac
  done

  if [[ ${#requested_tests[@]} -gt 0 ]]; then
    test_names=("${requested_tests[@]}")
  else
    mapfile -t test_names < <(declare -F | awk '{print $3}' | rg '^test_' | rg -v '^test_(run|pass|fail)$' | sort)
  fi

  if [[ ${#test_names[@]} -eq 0 ]]; then
    printf 'No test functions found.\n' >&2
    return 1
  fi

  suite_start="$(bash_test_now)"
  for test_name in "${test_names[@]}"; do
    if ! declare -F "$test_name" >/dev/null 2>&1; then
      test_fail "missing test function" "Unknown test: [$test_name]"
    fi
    run_test_function "$test_name"
  done
  suite_end="$(bash_test_now)"

  suite_elapsed="$(LC_ALL=C awk -v s="$suite_start" -v e="$suite_end" 'BEGIN { printf "%.2f", e - s }')"
  bash_test_color_print "${BASH_TEST_C_BOLD}${BASH_TEST_C_GREEN}" \
    "SUMMARY: ${BASH_TEST_CASES} test(s), ${BASH_TEST_ASSERTIONS} assertion(s), ${suite_elapsed}s"
}

# make_tool_dir DIR [TOOL...]: create DIR holding symlinks to the basic tools our scripts need (bash, env,
# coreutils, find, ...; resolved with `command -v` at call time, so it works for brew bash on macOS and
# /usr/bin on Linux alike) plus any extra TOOLs named. Use `PATH=DIR[:stubs]` as the whole PATH to get an
# environment that reliably lacks everything else (e.g. go, shellcheck), wherever the real ones live.
make_tool_dir() {
  local dir="$1" tool path
  shift
  mkdir -p "$dir"
  for tool in bash env dirname basename readlink find sort cat tr mkdir rm uname sed grep head wc mktemp cp chmod ln pwd printf test dd sleep touch "$@"; do
    path="$(command -v "$tool" 2>/dev/null || true)"
    [[ "$path" == /* ]] || continue
    ln -sf "$path" "$dir/$tool"
  done
}
