#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
INSTALL="$PROJECT_ROOT/bin/install"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; TEST_TMP=""; return 0; }
trap 'cleanup_test_tmp' EXIT

# Fake repo: bin/install plus a bin/build that only records its argv; a stub go on PATH.
setup_repo() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP/repo"
  mkdir -p "$REPO/bin" "$TEST_TMP/stubs" "$TEST_TMP/empty"
  cp "$INSTALL" "$REPO/bin/install"
  printf '#!/usr/bin/env bash\necho "build $*" >>"$TEST_TMP/calls.log"\n' >"$REPO/bin/build"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$TEST_TMP/stubs/go"
  chmod +x "$REPO/bin/install" "$REPO/bin/build" "$TEST_TMP/stubs/go"
  : >"$TEST_TMP/calls.log"
  export TEST_TMP
}

run_install() {
  capture_command OUT RC env PATH="$TEST_TMP/stubs:$(dirname "$(command -v bash)"):/usr/bin:/bin" "$REPO/bin/install" "$@"
}

test_delegates_to_build_install() {
  setup_repo
  run_install
  assert_status 0 "$RC" "install succeeds"
  assert_eq "build --install" "$(cat "$TEST_TMP/calls.log")" "no flags: bin/build --install"
}

test_passes_prefix_and_force() {
  setup_repo
  run_install --force --prefix "$TEST_TMP/p"
  assert_eq "build --install --force --prefix $TEST_TMP/p" "$(cat "$TEST_TMP/calls.log")" "flags reach bin/build"
}

test_prefix_without_value_is_usage_error() {
  setup_repo
  run_install --prefix
  assert_status 2 "$RC" "--prefix without DIR exits 2"
  assert_contains "$OUT" "--prefix needs a directory" "says why"
  assert_eq "" "$(cat "$TEST_TMP/calls.log")" "nothing was built"
}

test_unknown_flag_and_help() {
  setup_repo
  run_install --bogus
  assert_status 2 "$RC" "unknown flag exits 2"
  assert_contains "$OUT" "unknown option: --bogus" "unknown flag is named"
  run_install --help
  assert_status 0 "$RC" "--help exits 0"
  assert_contains "$OUT" "Usage: bin/install" "--help prints usage"
}

test_missing_go_exits_127_with_hint() {
  setup_repo
  make_tool_dir "$TEST_TMP/empty"
  capture_command OUT RC env PATH="$TEST_TMP/empty" "$REPO/bin/install"
  assert_status 127 "$RC" "missing go exits 127"
  assert_contains "$OUT" "needs Go" "message names Go"
  assert_contains "$OUT" "install golang-go" "hint covers apt"
  assert_contains "$OUT" "brew install go" "hint covers brew"
  assert_eq "" "$(cat "$TEST_TMP/calls.log")" "nothing was built"
}

run_tests "$@"
