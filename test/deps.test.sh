#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
DEPS="$PROJECT_ROOT/bin/deps"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
trap 'cleanup_test_tmp' EXIT

# A fake repo whose bin/deps is the real script and whose PATH holds only basic tools and a stubbed go.
setup_repo() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP/repo"
  mkdir -p "$REPO/bin"
  link_script "$DEPS" "$REPO/bin/deps"
  add_style_lib "$REPO/bin"
  printf 'github.com/x/y v1.0.0 -> v1.2.0\n' >"$TEST_TMP/outdated"
  make_tool_dir "$TEST_TMP/tools"
  use_stubs "$TEST_TMP/tools" go-stub=go
  export STUB_LOG="$TEST_TMP/go.log"
  : >"$STUB_LOG"
}

run_deps() {
  capture_command OUT RC env PATH="$TEST_TMP/tools" STUB_LOG="$STUB_LOG" STUB_GO_OUT="$TEST_TMP/outdated" "$REPO/bin/deps" "$@"
}

test_lists_outdated_and_changes_nothing() {
  setup_repo
  run_deps
  assert_status 0 "$RC" "list exits 0"
  assert_contains "$OUT" "github.com/x/y v1.0.0 -> v1.2.0" "prints the stubbed go list output"
  assert_eq "1" "$(wc -l <"$STUB_LOG" | tr -d ' ')" "exactly one go call"
  assert_contains "$(cat "$STUB_LOG")" "go list -m -u" "it is a read-only go list"
}

test_patch_and_minor_update_then_tidy() {
  setup_repo
  run_deps --patch
  assert_status 0 "$RC" "--patch exits 0"
  assert_eq "go get -u=patch ./...|go mod tidy" "$(paste -sd'|' "$STUB_LOG")" "--patch runs go get -u=patch, then tidy"
  assert_contains "$OUT" "commit by hand" "tells the user to commit"
  : >"$STUB_LOG"
  run_deps --minor
  assert_eq "go get -u ./...|go mod tidy" "$(paste -sd'|' "$STUB_LOG")" "--minor runs go get -u, then tidy"
}

test_major_only_lists() {
  setup_repo
  run_deps --major
  assert_status 0 "$RC" "--major exits 0"
  assert_contains "$OUT" "by hand" "explains major updates are manual"
  assert_not_contains "$(cat "$STUB_LOG")" "go get" "no go get for --major"
}

test_never_commits() {
  setup_repo
  git -C "$REPO" init -q
  git -C "$REPO" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m init
  run_deps --minor
  assert_eq "1" "$(git -C "$REPO" rev-list --count HEAD)" "no commit was made"
}

test_flags_and_missing_go() {
  setup_repo
  run_deps --help
  assert_status 0 "$RC" "--help exits 0"
  assert_contains "$OUT" "Usage: bin/deps" "--help prints usage"
  run_deps --bogus
  assert_status 2 "$RC" "unknown option exits 2"
  run_deps --patch --minor
  assert_status 2 "$RC" "two levels exit 2"
  capture_command OUT RC env PATH="$TEST_TMP/empty" "$REPO/bin/deps"
  assert_status 127 "$RC" "missing go exits 127"
}

run_tests "$@"
