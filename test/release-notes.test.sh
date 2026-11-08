#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$PROJECT_ROOT/bin/release-notes"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; return 0; }
trap 'cleanup_test_tmp' EXIT

setup_changelog() {
  TEST_TMP="$(mktemp -d)"
  cat >"$TEST_TMP/CHANGELOG.md" <<'MD'
# Changelog

## [Unreleased]

## [1.0.0] - 2026-12-01

### Added
- Stable thing.

## [1.0.0-rc.2] - 2026-11-09

### Fixed
- Second fix.

### Added
- Second feature.

## [1.0.0-rc.1] - 2026-11-01

### Added
- First feature.
MD
}

run_script() { "$SCRIPT" --changelog "$TEST_TMP/CHANGELOG.md" "$@"; }

test_strips_leading_v_and_prints_section() {
  setup_changelog
  local out
  out="$(run_script v1.0.0-rc.2)"
  assert_contains "$out" "### Fixed" "section body printed"
  assert_contains "$out" "- Second feature." "last line of section printed"
}

test_stops_at_next_heading() {
  setup_changelog
  local out
  out="$(run_script 1.0.0-rc.2)"
  assert_not_contains "$out" "First feature" "next section excluded"
  assert_not_contains "$out" "## [" "no headings in output"
}

test_exact_match_not_prefix() {
  setup_changelog
  local out
  out="$(run_script v1.0.0)"
  assert_contains "$out" "Stable thing" "1.0.0 section"
  assert_not_contains "$out" "Second fix" "rc.2 not mixed into 1.0.0"
}

test_output_is_trimmed() {
  setup_changelog
  local out first
  out="$(run_script v1.0.0-rc.1)"
  first="$(printf '%s\n' "$out" | head -n 1)"
  assert_eq "### Added" "$first" "no leading blank line"
  assert_eq "- First feature." "$(printf '%s\n' "$out" | tail -n 1)" "last line is content"
}

test_missing_version_exits_1() {
  setup_changelog
  local status=0 err
  err="$(run_script v9.9.9 2>&1 >/dev/null)" || status=$?
  assert_eq "1" "$status" "exit 1"
  assert_contains "$err" "no CHANGELOG section for 9.9.9" "message"
}

test_empty_section_exits_1() {
  setup_changelog
  local status=0
  run_script v-unreleased >/dev/null 2>&1 || status=$?
  assert_eq "1" "$status" "unknown tag exits 1"
  status=0
  run_script Unreleased >/dev/null 2>&1 || status=$?
  assert_eq "1" "$status" "empty Unreleased section exits 1"
}

test_usage_errors_exit_2() {
  setup_changelog
  local status=0
  "$SCRIPT" >/dev/null 2>&1 || status=$?
  assert_eq "2" "$status" "no tag"
  status=0
  "$SCRIPT" --bogus >/dev/null 2>&1 || status=$?
  assert_eq "2" "$status" "unknown option"
}

test_missing_file_exits_1() {
  TEST_TMP="$(mktemp -d)"
  local status=0
  "$SCRIPT" --changelog "$TEST_TMP/nope.md" v1.0.0 >/dev/null 2>&1 || status=$?
  assert_eq "1" "$status" "missing file"
}

run_tests "$@"
