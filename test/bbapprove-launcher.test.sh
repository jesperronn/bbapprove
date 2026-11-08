#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LAUNCHER="$PROJECT_ROOT/bin/bbapprove"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; return 0; }
trap 'cleanup_test_tmp' EXIT

# Fake repo: launcher + go.mod + one Go package, and a stub `go` that records its argv
# and writes a tiny shell script as the "binary" at the -o path.
setup_repo() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP/repo"; GO_LOG="$TEST_TMP/go.log"; : >"$GO_LOG"
  mkdir -p "$REPO/bin" "$REPO/cmd/bbapprove" "$REPO/internal/x" "$TEST_TMP/stubs" "$TEST_TMP/empty"
  cp "$LAUNCHER" "$REPO/bin/bbapprove"; chmod +x "$REPO/bin/bbapprove"
  # Stub bin/build: the launcher delegates to it as `bin/build -o FILE`; it runs the (stubbed) go build.
  printf '#!/usr/bin/env bash\ncd "$(dirname "$0")/.." && go build -o "$2" ./cmd/bbapprove\n' >"$REPO/bin/build"
  chmod +x "$REPO/bin/build"
  printf 'module bbapprove\n\ngo 1.27\n' >"$REPO/go.mod"
  printf 'package main\n\nfunc main() {}\n' >"$REPO/cmd/bbapprove/main.go"
  printf 'package x\n' >"$REPO/internal/x/x.go"
  cat >"$TEST_TMP/stubs/go" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$GO_LOG"
if [[ -n "${STUB_GO_FAIL:-}" ]]; then echo "go: boom" >&2; exit 3; fi
out=""; prev=""
for a in "$@"; do [[ "$prev" == -o ]] && out="$a"; prev="$a"; done
printf '#!/usr/bin/env bash\nprintf "argv:"; printf " [%%s]" "$@"; echo\n' >"$out"
chmod +x "$out"
STUB
  chmod +x "$TEST_TMP/stubs/go"
}

# run_launcher OUT_VAR RC_VAR [ENV=VAL...] -- LAUNCHER_PATH ARGS...
run_launcher() {
  local out_var="$1" rc_var="$2"; shift 2
  local -a envs=()
  while [[ $# -gt 0 && "$1" != "--" ]]; do envs+=("$1"); shift; done
  shift
  capture_command "$out_var" "$rc_var" env PATH="$TEST_TMP/stubs:/usr/bin:/bin" GO_LOG="$GO_LOG" "${envs[@]}" "$@"
}

test_missing_binary_builds_once_then_execs() {
  setup_repo
  local out rc
  run_launcher out rc -- "$REPO/bin/bbapprove" --apply "x y" --disallow a,b
  assert_eq "$rc" "0" "first run exits 0 after building"
  assert_eq "$(wc -l <"$GO_LOG" | tr -d ' ')" "1" "missing binary triggers exactly one go build"
  assert_eq "$(cat "$GO_LOG")" "build -o $REPO/bin/bbapprove-go ./cmd/bbapprove" "go build uses the right -o path and package"
  assert_contains "$out" "argv: [--apply] [x y] [--disallow] [a,b]" "args are passed through unchanged"
  cleanup_test_tmp; TEST_TMP=""
}

test_second_run_does_not_rebuild() {
  setup_repo
  local out rc
  run_launcher out rc -- "$REPO/bin/bbapprove" >/dev/null
  run_launcher out rc -- "$REPO/bin/bbapprove" --help
  assert_eq "$(wc -l <"$GO_LOG" | tr -d ' ')" "1" "second run does not rebuild"
  assert_contains "$out" "argv: [--help]" "second run still execs the binary"
  cleanup_test_tmp; TEST_TMP=""
}

test_touched_go_source_triggers_rebuild() {
  setup_repo
  local out rc
  run_launcher out rc -- "$REPO/bin/bbapprove" >/dev/null
  touch -t 209901010000 "$REPO/internal/x/x.go"
  run_launcher out rc -- "$REPO/bin/bbapprove" >/dev/null
  assert_eq "$(wc -l <"$GO_LOG" | tr -d ' ')" "2" "newer .go file under internal/ triggers a rebuild"
  cleanup_test_tmp; TEST_TMP=""
}

test_touched_go_mod_triggers_rebuild() {
  setup_repo
  local out rc
  run_launcher out rc -- "$REPO/bin/bbapprove" >/dev/null
  touch -t 209901010000 "$REPO/go.mod"
  run_launcher out rc -- "$REPO/bin/bbapprove" >/dev/null
  assert_eq "$(wc -l <"$GO_LOG" | tr -d ' ')" "2" "newer go.mod triggers a rebuild"
  cleanup_test_tmp; TEST_TMP=""
}

test_missing_go_exits_127() {
  setup_repo
  local out rc
  run_launcher out rc PATH="$TEST_TMP/empty:/usr/bin:/bin" -- "$REPO/bin/bbapprove" --help
  assert_eq "$rc" "127" "no go on PATH with missing binary exits 127"
  assert_contains "$out" "bbapprove: needs Go to build (https://go.dev/dl/)" "no-go message names the install URL"
  assert_eq "$(wc -l <"$GO_LOG" | tr -d ' ')" "0" "no go build attempted without go"
  cleanup_test_tmp; TEST_TMP=""
}

test_failing_build_propagates_and_does_not_exec() {
  setup_repo
  local out rc
  run_launcher out rc STUB_GO_FAIL=1 -- "$REPO/bin/bbapprove" --help
  assert_eq "$rc" "3" "failing go build exits with go's status"
  assert_contains "$out" "go: boom" "go's stderr is visible"
  assert_not_contains "$out" "argv:" "binary is not exec'd after a failed build"
  cleanup_test_tmp; TEST_TMP=""
}

test_works_through_symlink() {
  setup_repo
  local out rc
  mkdir -p "$TEST_TMP/abs" "$TEST_TMP/rel"
  ln -s "$REPO/bin/bbapprove" "$TEST_TMP/abs/bbapprove"
  ln -s "../repo/bin/bbapprove" "$TEST_TMP/rel/bbapprove"
  run_launcher out rc -- "$TEST_TMP/abs/bbapprove" --apply
  assert_eq "$(cat "$GO_LOG")" "build -o $REPO/bin/bbapprove-go ./cmd/bbapprove" "absolute symlink resolves to the real repo root"
  assert_contains "$out" "argv: [--apply]" "absolute symlink execs the binary"
  run_launcher out rc -- "$TEST_TMP/rel/bbapprove" --help
  assert_eq "$(wc -l <"$GO_LOG" | tr -d ' ')" "1" "relative symlink finds the built binary without rebuilding"
  assert_contains "$out" "argv: [--help]" "relative symlink execs the binary"
  cleanup_test_tmp; TEST_TMP=""
}

run_tests "$@"
