#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="$PROJECT_ROOT/bin/build"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
STUBS=""
GO_STUBS=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  return 0
}
cleanup_suite_tmp() {
  cleanup_test_tmp
  [[ -z "$STUBS" ]] || rm -rf "$STUBS" "$GO_STUBS"
  return 0
}
trap 'cleanup_suite_tmp' EXIT

REAL_GO="$(command -v go)"
REAL_GO_DIR="$(dirname "$REAL_GO")"

# Fake repo: bin/build + a marker launcher + a tiny Go program that prints its injected version.
# The stub `git` (test/fixtures/git, linked once per suite in STUBS) answers `describe` from STUB_GIT_DESCRIBE, or
# fails when STUB_GIT_FAIL is set. The `go` fixture (GO_STUBS, only on PATH where a test asks for it) passes
# through to the real go. bin/build is hardlinked from the real one (never written to).
setup_repo() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP/repo"
  PREFIX="$TEST_TMP/prefix"
  mkdir -p "$REPO/bin" "$REPO/cmd/bbapprove" "$TEST_TMP/empty" "$TEST_TMP/other"
  if [[ -z "$STUBS" ]]; then
    STUBS="$(mktemp -d)"
    use_stubs "$STUBS" git
    GO_STUBS="$(mktemp -d)"
    use_stubs "$GO_STUBS" go
  fi
  link_script "$BUILD" "$REPO/bin/build"
  add_style_lib "$REPO/bin"
  printf '#!/usr/bin/env bash\necho launcher\n' >"$REPO/bin/bbapprove"
  chmod +x "$REPO/bin/bbapprove"
  printf 'module bbapprove\n\ngo 1.21\n' >"$REPO/go.mod"
  cat >"$REPO/cmd/bbapprove/main.go" <<'GO'
package main

import "fmt"

var version = "dev"

func main() { fmt.Println("bbapprove " + version) }
GO
}

# run_build OUT_VAR RC_VAR [ENV=VAL...] -- ARGS...   (stub git first on PATH, real go reachable)
run_build() {
  local out_var="$1" rc_var="$2"
  shift 2
  local -a envs=()
  while [[ $# -gt 0 && "$1" != "--" ]]; do
    envs+=("$1")
    shift
  done
  shift
  capture_command "$out_var" "$rc_var" env PATH="$STUBS:$REAL_GO_DIR:/usr/bin:/bin" "${envs[@]}" "$REPO/bin/build" "$@"
}

test_git_describe_version_is_embedded() {
  setup_repo
  local out rc
  run_build out rc STUB_GIT_DESCRIBE=v1.0.0-rc.1-3-gabc123-dirty -- -o "$TEST_TMP/bb"
  assert_eq "$rc" "0" "build succeeds"
  assert_contains "$out" "built $TEST_TMP/bb (v1.0.0-rc.1-3-gabc123-dirty)" "success message names path and version"
  assert_eq "$("$TEST_TMP/bb" --version)" "bbapprove v1.0.0-rc.1-3-gabc123-dirty" "binary embeds the git describe output verbatim"
  cleanup_test_tmp
  TEST_TMP=""
}

test_default_output_path_is_bin_bbapprove_go() {
  setup_repo
  local out rc
  run_build out rc STUB_GIT_DESCRIBE=v2.0.0 --
  assert_eq "$rc" "0" "default build succeeds"
  assert_eq "$([[ -x "$REPO/bin/bbapprove-go" ]] && echo yes || echo no)" "yes" "default output is bin/bbapprove-go"
  cleanup_test_tmp
  TEST_TMP=""
}

test_build_keeps_stdout_clean() {
  setup_repo
  local stdout
  stdout="$(env PATH="$STUBS:$REAL_GO_DIR:/usr/bin:/bin" STUB_GIT_DESCRIBE=v1 "$REPO/bin/build" -o "$TEST_TMP/bb" 2>/dev/null)"
  assert_eq "$stdout" "" "build prints nothing on stdout"
  cleanup_test_tmp
  TEST_TMP=""
}

test_env_override_beats_git() {
  setup_repo
  local out rc
  run_build out rc STUB_GIT_DESCRIBE=v1.0.0 BBAPPROVE_VERSION=9.9.9-custom -- -o "$TEST_TMP/bb"
  assert_eq "$("$TEST_TMP/bb" --version)" "bbapprove 9.9.9-custom" "BBAPPROVE_VERSION replaces the git version"
  cleanup_test_tmp
  TEST_TMP=""
}

test_no_git_falls_back_to_dev() {
  setup_repo
  local out rc
  run_build out rc STUB_GIT_FAIL=1 -- -o "$TEST_TMP/bb"
  assert_eq "$rc" "0" "build works without a usable git"
  assert_eq "$("$TEST_TMP/bb" --version)" "bbapprove dev" "no git -> dev"
  cleanup_test_tmp
  TEST_TMP=""
}

test_version_flag_prints_without_building() {
  setup_repo
  local out rc
  run_build out rc STUB_GIT_DESCRIBE=v1.2.3 -- --version
  assert_eq "$rc" "0" "--version exits 0"
  assert_eq "$out" "v1.2.3" "--version prints exactly the version"
  assert_eq "$([[ -e "$REPO/bin/bbapprove-go" ]] && echo built || echo none)" "none" "--version does not build"
  run_build out rc PATH="$STUBS:/usr/bin:/bin" STUB_GIT_DESCRIBE=v1.2.3 -- --version
  assert_eq "$out" "v1.2.3" "--version works without go"
  cleanup_test_tmp
  TEST_TMP=""
}

test_install_creates_launcher_symlink_idempotently() {
  setup_repo
  local out rc
  run_build out rc STUB_GIT_DESCRIBE=v1 -- --install --prefix "$PREFIX" -o "$TEST_TMP/bb"
  assert_eq "$rc" "0" "install succeeds"
  assert_eq "$(readlink "$PREFIX/bbapprove")" "$REPO/bin/bbapprove" "symlink points at the launcher, not the Go binary"
  run_build out rc STUB_GIT_DESCRIBE=v1 -- --install --prefix "$PREFIX" -o "$TEST_TMP/bb"
  assert_eq "$rc" "0" "second install succeeds"
  assert_contains "$out" "already installed" "second install is a no-op"
  assert_eq "$(readlink "$PREFIX/bbapprove")" "$REPO/bin/bbapprove" "symlink unchanged"
  cleanup_test_tmp
  TEST_TMP=""
}

test_install_refuses_foreign_file_without_force() {
  setup_repo
  local out rc
  mkdir -p "$PREFIX"
  echo mine >"$PREFIX/bbapprove"
  run_build out rc STUB_GIT_DESCRIBE=v1 -- --install --prefix "$PREFIX" -o "$TEST_TMP/bb"
  assert_not_status "0" "$rc" "foreign file makes install fail"
  assert_contains "$out" "use --force" "refusal mentions --force"
  assert_eq "$(cat "$PREFIX/bbapprove")" "mine" "foreign file left untouched"
  run_build out rc STUB_GIT_DESCRIBE=v1 -- --install --force --prefix "$PREFIX" -o "$TEST_TMP/bb"
  assert_eq "$rc" "0" "--force replaces it"
  assert_eq "$(readlink "$PREFIX/bbapprove")" "$REPO/bin/bbapprove" "forced symlink points at the launcher"
  cleanup_test_tmp
  TEST_TMP=""
}

test_install_warns_when_another_bbapprove_shadows() {
  setup_repo
  local out rc
  printf '#!/usr/bin/env bash\n' >"$TEST_TMP/other/bbapprove"
  chmod +x "$TEST_TMP/other/bbapprove"
  capture_command out rc env PATH="$TEST_TMP/other:$STUBS:$REAL_GO_DIR:/usr/bin:/bin" STUB_GIT_DESCRIBE=v1 \
    "$REPO/bin/build" --install --prefix "$PREFIX" -o "$TEST_TMP/bb"
  assert_eq "$rc" "0" "shadowing is non-fatal"
  assert_contains "$out" "warning: 'bbapprove' on PATH resolves to $TEST_TMP/other/bbapprove" "warning names the winner"
  capture_command out rc env PATH="$PREFIX:$STUBS:$REAL_GO_DIR:/usr/bin:/bin" STUB_GIT_DESCRIBE=v1 \
    "$REPO/bin/build" --install --prefix "$PREFIX" -o "$TEST_TMP/bb"
  assert_not_contains "$out" "warning" "no warning when the installed link wins"
  cleanup_test_tmp
  TEST_TMP=""
}

test_test_flag_aborts_build_when_vet_fails() {
  setup_repo
  local out rc
  run_build out rc PATH="$GO_STUBS:$STUBS:$REAL_GO_DIR:/usr/bin:/bin" STUB_REAL_GO="$REAL_GO" STUB_LOG="$TEST_TMP/go.log" STUB_GO_VET_FAIL=1 \
    STUB_GIT_DESCRIBE=v1 -- --test -o "$TEST_TMP/bb"
  assert_not_status "0" "$rc" "failing vet fails the build"
  assert_contains "$out" "vet: boom" "vet output is visible"
  assert_not_contains "$out" "built " "no build after failed checks"
  assert_eq "$([[ -e "$TEST_TMP/bb" ]] && echo built || echo none)" "none" "no binary produced"
  cleanup_test_tmp
  TEST_TMP=""
}

test_test_flag_runs_checks_then_builds() {
  setup_repo
  local out rc
  printf 'package main\n\nimport "testing"\n\nfunc TestX(t *testing.T) {}\n' >"$REPO/cmd/bbapprove/main_test.go"
  # STUB_GO_FAST_TEST: `go test` runs without -race and with -run '^$' (it still compiles the test), the
  # racing run of a package with one empty test is ~2 s of pure compile time and proves nothing more.
  local -a fast=(PATH="$GO_STUBS:$STUBS:$REAL_GO_DIR:/usr/bin:/bin" STUB_REAL_GO="$REAL_GO" STUB_GO_FAST_TEST=1 STUB_GIT_DESCRIBE=v1)
  run_build out rc "${fast[@]}" -- --test -o "$TEST_TMP/bb"
  assert_eq "$rc" "0" "checks pass and build succeeds"
  assert_contains "$out" "built $TEST_TMP/bb (v1)" "build happens after checks"
  printf 'package main\n  var   x   =   1\n' >"$REPO/cmd/bbapprove/ugly.go"
  run_build out rc "${fast[@]}" -- --test -o "$TEST_TMP/bb2"
  assert_not_status "0" "$rc" "unformatted Go fails --test"
  assert_contains "$out" "ugly.go" "gofmt names the offending file"
  cleanup_test_tmp
  TEST_TMP=""
}

test_missing_go_exits_127() {
  setup_repo
  local out rc
  make_tool_dir "$TEST_TMP/empty"
  run_build out rc PATH="$TEST_TMP/empty" -- -o "$TEST_TMP/bb"
  assert_eq "$rc" "127" "no go exits 127"
  assert_contains "$out" "bbapprove: needs Go to build (https://go.dev/dl/)" "message names the install URL"
  cleanup_test_tmp
  TEST_TMP=""
}

run_tests "$@"
