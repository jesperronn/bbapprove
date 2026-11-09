#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
LINT="$PROJECT_ROOT/bin/lint"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

ORIG_PATH="$PATH"
TEST_TMP=""
PROJ=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; TEST_TMP=""; PATH="$ORIG_PATH"; return 0; }
trap 'cleanup_test_tmp' EXIT

LINT_OUT=""
LINT_STATUS=0
LINT_PATH=""

# Fake project with bin/lint copied in, a mix of shell and Go files, and stubs for
# the three external tools (on PATH) that only record their argv to LINT_TEST_LOG.
setup_fixture() {
  TEST_TMP="$(mktemp -d)"
  PROJ="$TEST_TMP/proj"
  mkdir -p "$PROJ/bin" "$PROJ/adapters" "$PROJ/test/lib" "$PROJ/internal/pkg" "$TEST_TMP/stubs"
  link_script "$LINT" "$PROJ/bin/lint"
  printf '#!/usr/bin/env bash\necho tool\n' >"$PROJ/bin/tool"
  printf 'not a script\n' >"$PROJ/bin/data.bin"
  printf '#!/usr/bin/env bash\necho adapter\n' >"$PROJ/adapters/x"
  printf '#!/usr/bin/env bash\n' >"$PROJ/test/a.test.sh"
  printf '# shellcheck shell=bash\n' >"$PROJ/test/lib/h.sh"
  printf 'package main\n' >"$PROJ/main.go"
  printf 'package pkg\n' >"$PROJ/internal/pkg/p.go"
  printf '# readme\n' >"$PROJ/README.md"

  # Fixed recording stubs from test/fixtures, symlinked; they log "NAME ARGS" to $STUB_LOG.
  use_stubs "$TEST_TMP/stubs" recorder=shellcheck recorder=gofmt go-stub=go
  export STUB_LOG="$TEST_TMP/calls.log"

  export LINT_TEST_LOG="$STUB_LOG"
  : >"$LINT_TEST_LOG"
  export PATH="$TEST_TMP/stubs:$ORIG_PATH"
  unset STUB_SHELLCHECK_RC STUB_GOFMT_OUT STUB_GOFMT_RC STUB_GO_RC
  LINT_PATH=""
}

# run_lint ARGS...: run bin/lint from the fixture project; sets LINT_OUT and LINT_STATUS.
run_lint() {
  LINT_STATUS=0
  LINT_OUT="$(cd "$PROJ" && env PATH="${LINT_PATH:-$PATH}" bash bin/lint "$@" 2>&1)" || LINT_STATUS=$?
}

test_help_exits_zero() {
  setup_fixture
  run_lint --help
  assert_status 0 "$LINT_STATUS" "--help exits 0"
  assert_contains "$LINT_OUT" "Usage: bin/lint" "--help prints usage"
  cleanup_test_tmp
}

test_no_args_lints_whole_project() {
  setup_fixture
  run_lint
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "no args exits 0 when every tool passes"
  assert_contains "$log" "shellcheck bin/lint bin/tool adapters/x test/a.test.sh test/lib/h.sh" "no args: shellcheck gets every bash script in bin, adapters, test"
  assert_not_contains "$log" "data.bin" "no args: non-shell file in bin is not shellchecked"
  assert_contains "$log" "gofmt -l ." "no args: gofmt -l ."
  assert_contains "$log" "go vet ./..." "no args: go vet ./..."
  assert_contains "$LINT_OUT" "shellcheck: 5 file(s) ok" "no args: shellcheck summary line"
  assert_contains "$LINT_OUT" "gofmt ok" "no args: gofmt summary line"
  assert_contains "$LINT_OUT" "go vet ok" "no args: go vet summary line"
  cleanup_test_tmp
}

test_path_args_lint_only_those() {
  setup_fixture
  run_lint internal/pkg/p.go test/a.test.sh
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "path args exit 0 when clean"
  assert_contains "$log" "shellcheck test/a.test.sh" "path args: shell file goes to shellcheck"
  assert_contains "$log" "gofmt -l internal/pkg/p.go" "path args: Go file goes to gofmt -l"
  assert_contains "$log" "go vet ./internal/pkg" "path args: owning package goes to go vet"
  assert_not_contains "$log" "bin/tool" "path args: unrelated shell file not linted"
  assert_not_contains "$log" "main.go" "path args: unrelated Go file not linted"
  cleanup_test_tmp
}

test_directory_arg_expands_recursively() {
  setup_fixture
  run_lint internal
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_contains "$log" "gofmt -l internal/pkg/p.go" "directory arg: Go files found recursively"
  assert_contains "$log" "go vet ./internal/pkg" "directory arg: package vetted"
  assert_not_contains "$log" "shellcheck" "directory arg: no shell files, so no shellcheck"
  cleanup_test_tmp
}

test_unknown_file_skipped_with_note() {
  setup_fixture
  run_lint README.md test/a.test.sh
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "skipped file does not fail the run"
  assert_contains "$LINT_OUT" "lint: skipping README.md (not Go or shell)" "unknown file gets a skip note"
  assert_contains "$log" "shellcheck test/a.test.sh" "shell file alongside it is still linted"
  assert_not_contains "$log" "gofmt" "no Go targets, so gofmt is not run"
  cleanup_test_tmp
}

test_missing_path_exits_2() {
  setup_fixture
  run_lint nope.go
  assert_status 2 "$LINT_STATUS" "missing path exits 2"
  assert_contains "$LINT_OUT" "lint: no such file or directory: nope.go" "missing path is reported"
  cleanup_test_tmp
}

test_autofix_uses_gofmt_w() {
  setup_fixture
  run_lint --autofix internal/pkg/p.go
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "--autofix exits 0"
  assert_contains "$log" "gofmt -w internal/pkg/p.go" "--autofix runs gofmt -w on the Go targets"
  assert_not_contains "$log" "gofmt -l" "--autofix does not report-only with gofmt -l"
  cleanup_test_tmp
}

test_failing_tool_does_not_stop_later_tools() {
  setup_fixture
  export STUB_SHELLCHECK_RC=1
  run_lint
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 1 "$LINT_STATUS" "failing shellcheck makes exit 1"
  assert_contains "$LINT_OUT" "shellcheck: 5 file(s) FAILED" "failing shellcheck is reported"
  assert_contains "$log" "gofmt -l ." "gofmt still runs after shellcheck fails"
  assert_contains "$log" "go vet ./..." "go vet still runs after shellcheck fails"
  assert_contains "$LINT_OUT" "lint: FAILED: shellcheck" "final line names the failed tool"
  cleanup_test_tmp
}

test_gofmt_listing_fails_run() {
  setup_fixture
  export STUB_GOFMT_OUT="internal/pkg/p.go"
  run_lint internal
  assert_status 1 "$LINT_STATUS" "unformatted Go file makes exit 1"
  assert_contains "$LINT_OUT" "internal/pkg/p.go" "unformatted file is listed"
  assert_contains "$LINT_OUT" "gofmt: FAILED" "gofmt failure is reported"
  cleanup_test_tmp
}

test_missing_shellcheck_exits_127() {
  setup_fixture
  local only_go="$TEST_TMP/no-shellcheck"
  mkdir -p "$only_go"
  make_tool_dir "$only_go"
  cp "$TEST_TMP/stubs/go" "$TEST_TMP/stubs/gofmt" "$only_go/"
  LINT_PATH="$only_go"
  local found
  found="$(env PATH="$LINT_PATH" bash -c 'command -v shellcheck' || true)"
  assert_eq "" "$found" "fixture PATH has no shellcheck"
  run_lint
  assert_status 127 "$LINT_STATUS" "missing shellcheck exits 127"
  assert_contains "$LINT_OUT" "lint: shellcheck is not installed" "missing shellcheck is named"
  cleanup_test_tmp
}

test_missing_go_exits_127_only_when_go_needed() {
  setup_fixture
  local only_sc="$TEST_TMP/no-go"
  mkdir -p "$only_sc"
  make_tool_dir "$only_sc"
  cp "$TEST_TMP/stubs/shellcheck" "$only_sc/"
  LINT_PATH="$only_sc"
  local found
  found="$(env PATH="$LINT_PATH" bash -c 'command -v go' || true)"
  assert_eq "" "$found" "fixture PATH has no go"
  run_lint test/a.test.sh
  assert_status 0 "$LINT_STATUS" "shell-only targets do not need go"
  run_lint internal/pkg/p.go
  assert_status 127 "$LINT_STATUS" "Go targets without go exit 127"
  assert_contains "$LINT_OUT" "lint: go is not installed" "missing go is named"
  cleanup_test_tmp
}

test_binary_file_does_not_trigger_tr_error() {
  setup_fixture
  # BSD tr aborts with "Illegal byte sequence" on non-UTF-8 bytes under a UTF-8 locale.
  printf '\317\372\355\376 binary\n' >"$PROJ/bin/blob"
  LINT_STATUS=0
  LINT_OUT="$(cd "$PROJ" && env LC_ALL=en_US.UTF-8 PATH="$PATH" bash bin/lint bin 2>&1)" || LINT_STATUS=$?
  assert_status 0 "$LINT_STATUS" "a binary file in bin/ does not fail the run"
  assert_not_contains "$LINT_OUT" "Illegal byte sequence" "no tr error on binary bytes under a UTF-8 locale"
  assert_not_contains "$(<"$LINT_TEST_LOG")" "bin/blob" "the binary file is not handed to shellcheck"
  cleanup_test_tmp
}

run_tests "$@"
