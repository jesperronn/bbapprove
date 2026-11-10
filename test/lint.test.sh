#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
LINT="$PROJECT_ROOT/bin/lint"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

ORIG_PATH="$PATH"
TEST_TMP=""
PROJ=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  PROJ=""
  PATH="$ORIG_PATH"
  return 0
}
trap 'cleanup_test_tmp' EXIT

LINT_OUT=""
LINT_STATUS=0
LINT_PATH=""

# basic_tools_dir DIR: symlinks to the external tools bin/lint needs, and no lint tool. One subshell does
# all the lookups (command -v is a builtin), and one ln links them all.
basic_tools_dir() {
  local dir="$1" p
  local -a found=()
  mkdir -p "$dir"
  while IFS= read -r p; do
    [[ "$p" == /* ]] && found+=("$p")
  done < <(for t in bash env dirname basename find sort cat tr head; do command -v "$t" || true; done)
  ln -sf "${found[@]}" "$dir/"
}

# One fake project for the whole suite: bin/lint, shell and Go files, and the fixed recording stubs
# (test/fixtures recorder and go-stub) that log "NAME ARGS" to LINT_TEST_LOG. Built once, reset per test.
# The stub sets with one tool left out, and the basic-tools dir, are built here too.
setup_fixture() {
  TEST_TMP="$(mktemp -d)"
  PROJ="$TEST_TMP/proj"
  mkdir -p "$PROJ/bin" "$PROJ/adapters" "$PROJ/test/lib" "$PROJ/internal/pkg" "$TEST_TMP/stubs"
  link_script "$LINT" "$PROJ/bin/lint"
  add_style_lib "$PROJ/bin"
  printf '#!/usr/bin/env bash\necho tool\n' >"$PROJ/bin/tool"
  printf 'not a script\n' >"$PROJ/bin/data.bin"
  # Binary bytes that BSD tr rejects under a UTF-8 locale: lint must not choke on them.
  printf '\317\372\355\376 binary\n' >"$PROJ/bin/blob"
  printf '#!/usr/bin/env bash\necho adapter\n' >"$PROJ/adapters/x"
  printf '#!/usr/bin/env bash\n' >"$PROJ/test/a.test.sh"
  printf '# shellcheck shell=bash\n' >"$PROJ/test/lib/h.sh"
  printf 'package main\n' >"$PROJ/main.go"
  printf 'package pkg\n' >"$PROJ/internal/pkg/p.go"
  printf '# readme\n' >"$PROJ/README.md"

  use_stubs "$TEST_TMP/stubs" recorder=shellcheck recorder=shfmt recorder=gofmt go-stub=go
  basic_tools_dir "$TEST_TMP/basic"
  use_stubs "$TEST_TMP/no-shellcheck" recorder=gofmt go-stub=go
  use_stubs "$TEST_TMP/no-go" recorder=shellcheck
  use_stubs "$TEST_TMP/no-shfmt" recorder=shellcheck recorder=gofmt go-stub=go
  export STUB_LOG="$TEST_TMP/calls.log"
  export LINT_TEST_LOG="$STUB_LOG"
  export PATH="$TEST_TMP/stubs:$ORIG_PATH"
}

# reset_fixture: empty the call log and drop every per-test stub setting and PATH override.
reset_fixture() {
  : >"$LINT_TEST_LOG"
  LINT_PATH=""
  unset STUB_SHELLCHECK_RC STUB_SHFMT_OUT STUB_SHFMT_RC STUB_GOFMT_OUT STUB_GOFMT_RC STUB_GO_RC
}

# use_fixture: build the shared fixture on first use, then reset it for this test.
use_fixture() {
  [[ -n "$PROJ" ]] || setup_fixture
  reset_fixture
}

# run_lint ARGS...: run bin/lint from the fixture project under a UTF-8 locale; sets LINT_OUT and LINT_STATUS.
run_lint() {
  LINT_STATUS=0
  LINT_OUT="$(cd "$PROJ" && env LC_ALL=en_US.UTF-8 PATH="${LINT_PATH:-$PATH}" bash bin/lint "$@" 2>&1)" || LINT_STATUS=$?
}

# log_lines: number of tool calls logged by the last run.
log_lines() {
  awk 'END { print NR }' "$LINT_TEST_LOG"
}

test_whole_project_run_calls_each_tool_once() {
  use_fixture
  run_lint
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "no args exits 0 when every tool passes"
  assert_eq 4 "$(log_lines)" "no args: exactly four tool calls (shellcheck, shfmt, gofmt, go vet)"
  assert_contains "$log" "shellcheck bin/lint bin/tool bin/lib/style.sh adapters/x test/a.test.sh test/lib/h.sh" "no args: shellcheck gets every bash script in bin, adapters, test"
  assert_contains "$log" "shfmt -d bin/lint bin/tool bin/lib/style.sh adapters/x test/a.test.sh test/lib/h.sh" "no args: shfmt -d gets the same shell files"
  assert_contains "$log" "gofmt -l ." "no args: gofmt -l ."
  assert_contains "$log" "go vet ./..." "no args: go vet ./..."
  assert_not_contains "$log" "data.bin" "no args: non-shell file in bin is not shellchecked"
  assert_not_contains "$log" "blob" "no args: binary file in bin is not shellchecked"
  assert_not_contains "$LINT_OUT" "Illegal byte sequence" "no tr error on binary bytes under a UTF-8 locale"
  assert_contains "$LINT_OUT" "shellcheck: 6 file(s) ok" "no args: shellcheck summary line"
  assert_contains "$LINT_OUT" "shfmt ok" "no args: shfmt summary line"
  assert_contains "$LINT_OUT" "gofmt ok" "no args: gofmt summary line"
  assert_contains "$LINT_OUT" "go vet ok" "no args: go vet summary line"
}

test_failing_tools_do_not_stop_the_others() {
  use_fixture
  export STUB_SHELLCHECK_RC=1 STUB_SHFMT_OUT=$'-\techo tab\n+  echo spaces' STUB_SHFMT_RC=1
  export STUB_GOFMT_OUT="internal/pkg/p.go"
  run_lint test/a.test.sh internal/pkg/p.go
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 1 "$LINT_STATUS" "failing tools make exit 1"
  assert_contains "$LINT_OUT" "shellcheck: 1 file(s) FAILED" "failing shellcheck is reported"
  assert_contains "$LINT_OUT" "+  echo spaces" "the shfmt diff is printed"
  assert_contains "$LINT_OUT" "shfmt: needs formatting" "shfmt failure is reported"
  assert_contains "$LINT_OUT" "internal/pkg/p.go" "the unformatted Go file is listed"
  assert_contains "$LINT_OUT" "gofmt: FAILED" "gofmt failure is reported"
  assert_contains "$log" "shfmt -d test/a.test.sh" "shfmt still runs after shellcheck fails"
  assert_contains "$log" "go vet ./internal/pkg" "go vet still runs after the other tools fail"
  assert_contains "$LINT_OUT" "lint: FAILED: shellcheck,shfmt,gofmt" "final line names every failed tool"
}

test_missing_required_tool_exits_127() {
  use_fixture
  LINT_PATH="$TEST_TMP/no-shellcheck:$TEST_TMP/basic"
  run_lint test/a.test.sh
  assert_status 127 "$LINT_STATUS" "missing shellcheck exits 127"
  assert_contains "$LINT_OUT" "lint: shellcheck is not installed" "missing shellcheck is named"
  assert_eq "" "$(<"$LINT_TEST_LOG")" "nothing runs before the missing tool is reported"

  LINT_PATH="$TEST_TMP/no-go:$TEST_TMP/basic"
  run_lint test/a.test.sh
  assert_status 0 "$LINT_STATUS" "shell-only targets do not need go"
  run_lint internal/pkg/p.go
  assert_status 127 "$LINT_STATUS" "Go targets without go exit 127"
  assert_contains "$LINT_OUT" "lint: go is not installed" "missing go is named"
}

test_missing_shfmt_is_a_note_and_dirs_are_searched() {
  use_fixture
  LINT_PATH="$TEST_TMP/no-shfmt:$TEST_TMP/basic"
  run_lint internal test/a.test.sh
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "missing shfmt does not change the exit status"
  assert_contains "$LINT_OUT" "shfmt: not installed, skipping (brew install shfmt)" "missing shfmt is noted"
  assert_eq 3 "$(log_lines)" "missing shfmt: shellcheck, gofmt and go vet still run, shfmt does not"
  assert_contains "$log" "shellcheck test/a.test.sh" "shell file in the path args is shellchecked"
  assert_contains "$log" "gofmt -l internal/pkg/p.go" "directory arg: Go files found recursively"
  assert_contains "$log" "go vet ./internal/pkg" "directory arg: package vetted"
}

test_autofix_runs_the_fixers() {
  use_fixture
  run_lint --autofix test/a.test.sh internal/pkg/p.go
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "--autofix exits 0"
  assert_contains "$log" "gofmt -w internal/pkg/p.go" "--autofix runs gofmt -w on the Go targets"
  assert_contains "$log" "shfmt -w test/a.test.sh" "--autofix runs shfmt -w on the shell targets"
  assert_not_contains "$log" "gofmt -l" "--autofix does not run gofmt -l"
  assert_not_contains "$log" "shfmt -w internal" "--autofix does not hand Go files to shfmt"
  assert_contains "$LINT_OUT" "shfmt ok (autofix applied)" "--autofix reports the shfmt result"

  reset_fixture
  export STUB_SHFMT_OUT="parse error" STUB_SHFMT_RC=1
  run_lint --autofix test/a.test.sh
  assert_status 1 "$LINT_STATUS" "a shfmt error during --autofix makes exit 1"
  assert_contains "$LINT_OUT" "shfmt: FAILED (autofix error)" "the autofix error is reported"
}

test_path_args_restrict_the_targets() {
  use_fixture
  run_lint test/a.test.sh internal/pkg/p.go README.md
  local log
  log="$(<"$LINT_TEST_LOG")"
  assert_status 0 "$LINT_STATUS" "path args exit 0 when clean"
  assert_contains "$log" "shellcheck test/a.test.sh" "path args: shell file goes to shellcheck"
  assert_contains "$log" "shfmt -d test/a.test.sh" "path args: shell file goes to shfmt"
  assert_contains "$log" "gofmt -l internal/pkg/p.go" "path args: Go file goes to gofmt -l"
  assert_contains "$log" "go vet ./internal/pkg" "path args: owning package goes to go vet"
  assert_not_contains "$log" "bin/tool" "path args: unrelated shell file not linted"
  assert_not_contains "$log" "main.go" "path args: unrelated Go file not linted"
  assert_not_contains "$log" "README.md" "path args: skipped file reaches no tool"
  assert_contains "$LINT_OUT" "lint: skipping README.md (not Go or shell)" "unknown file gets a skip note"

  run_lint nope.go
  assert_status 2 "$LINT_STATUS" "missing path exits 2"
  assert_contains "$LINT_OUT" "lint: no such file or directory: nope.go" "missing path is reported"
}

run_tests "$@"
