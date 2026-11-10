#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
SETUP="$PROJECT_ROOT/bin/setup"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
trap 'cleanup_test_tmp' EXIT

# Fake repo (go.mod needs 1.21) with bin/setup and a recording bin/build. PATH holds only basic tools
# (make_tool_dir) plus stub tools; stubs for the five required tools exist unless removed by a test.
setup_repo() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP/repo"
  TOOLS="$TEST_TMP/tools"
  mkdir -p "$REPO/bin"
  cp "$SETUP" "$REPO/bin/setup"
  add_style_lib "$REPO/bin"
  printf 'module bbapprove\n\ngo 1.21\n' >"$REPO/go.mod"
  use_stubs "$REPO/bin" recorder=build
  chmod +x "$REPO/bin/setup"
  make_tool_dir "$TOOLS" awk
  # The tools are fixed stubs from test/fixtures (symlinks): noop for git/jq/shellcheck/rg, go-stub for go.
  # go logs only `mod download`, bin/build logs "build ARGS", both to calls.log in call order.
  use_stubs "$TOOLS" noop=git noop=jq noop=shellcheck noop=rg go-stub=go
  export STUB_LOG="$TEST_TMP/calls.log" STUB_GO_LOG_MATCH='mod download' STUB_BUILD_LABEL=build
  : >"$TEST_TMP/calls.log"
  export TEST_TMP
}

test_check_passes_without_side_effects() {
  setup_repo
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup" --check
  assert_status 0 "$RC" "--check exits 0 when all required tools exist"
  assert_contains "$OUT" "ok       rg" "rg is reported ok"
  assert_contains "$OUT" "optional shfmt not found" "missing shfmt is only optional"
  assert_contains "$OUT" "optional gh not found" "missing gh is only optional"
  assert_contains "$OUT" "optional podman not found" "missing podman is only optional"
  assert_contains "$OUT" "go 1.22.3 (go.mod needs 1.21)" "go version is compared with go.mod"
  assert_eq "" "$(cat "$TEST_TMP/calls.log")" "--check downloads and builds nothing"
}

test_check_fails_with_install_hint_for_missing_tool() {
  setup_repo
  rm "$TOOLS/rg"
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup" --check
  assert_status 1 "$RC" "missing required tool exits 1"
  assert_contains "$OUT" "MISSING  rg" "rg is flagged"
  assert_contains "$OUT" "ripgrep" "hint names the package"
}

test_old_go_is_rejected() {
  setup_repo
  capture_command OUT RC env PATH="$TOOLS" STUB_GOVERSION=go1.20.9 "$REPO/bin/setup" --check
  assert_status 1 "$RC" "go older than go.mod exits 1"
  assert_contains "$OUT" "TOO OLD  go 1.20.9 found, go.mod needs 1.21" "message names both versions"
}

test_full_run_downloads_builds_and_prints_next_steps() {
  setup_repo
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup"
  assert_status 0 "$RC" "setup succeeds"
  assert_eq "go mod download|build " "$(paste -sd'|' "$TEST_TMP/calls.log")" "modules first, then bin/build"
  assert_contains "$OUT" "bin/test" "next steps: bin/test"
  assert_contains "$OUT" "bin/install" "next steps: bin/install"
  assert_contains "$OUT" "bin/help" "next steps: bin/help"
  assert_eq "no" "$([[ -e "$REPO/bbapprove.config.toml" ]] && echo yes || echo no)" "no config file is created"
}

test_idempotent() {
  setup_repo
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup"
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup"
  assert_status 0 "$RC" "second run succeeds too"
}

test_missing_tool_stops_before_download() {
  setup_repo
  rm "$TOOLS/shellcheck"
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup"
  assert_status 1 "$RC" "missing tool exits 1"
  assert_eq "" "$(cat "$TEST_TMP/calls.log")" "nothing downloaded or built"
}

test_flags() {
  setup_repo
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup" --help
  assert_status 0 "$RC" "--help exits 0"
  assert_contains "$OUT" "Usage: bin/setup" "--help prints usage"
  capture_command OUT RC env PATH="$TOOLS" "$REPO/bin/setup" --bogus
  assert_status 2 "$RC" "unknown flag exits 2"
  assert_contains "$OUT" "unknown option: --bogus" "unknown flag is named"
}

run_tests "$@"
