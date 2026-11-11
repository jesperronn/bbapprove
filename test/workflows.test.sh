#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
WF="$PROJECT_ROOT/bin/workflows"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
trap 'cleanup_test_tmp' EXIT

test_lists_every_script_with_a_description() {
  local out rc f name
  capture_command out rc "$WF"
  assert_status 0 "$rc" "bin/workflows exits 0"
  for f in "$PROJECT_ROOT"/bin/*; do
    [[ -f "$f" && -x "$f" && "$(head -c 2 "$f")" == "#!" ]] || continue
    name="$(basename "$f")"
    assert_contains "$out" "  $name " "bin/workflows lists $name"
    # Line "  <name>  <description>" must carry text after the name, not the fallback.
    if ! grep -qE "^  $name +[^ ]" <<<"$out"; then test_fail "$name has a description in bin/workflows"; fi
    test_pass "$name has a description"
  done
  assert_not_contains "$out" "(no description line)" "no script is missing its '# name — description' line"
  assert_not_contains "$out" "  bbapprove-go " "the compiled binary is not listed"
}

test_shows_the_standard_flow() {
  local out rc
  capture_command out rc "$WF"
  assert_contains "$out" "bin/setup" "flow mentions setup"
  assert_contains "$out" "bin/test" "flow mentions test"
  assert_contains "$out" "bin/install" "flow mentions install"
  assert_contains "$out" "bin/release X.Y.Z" "flow mentions release"
}

test_flags() {
  local out rc
  capture_command out rc "$WF" --help
  assert_status 0 "$rc" "--help exits 0"
  assert_contains "$out" "Usage: bin/workflows" "--help prints usage"
  capture_command out rc "$WF" --bogus
  assert_status 2 "$rc" "unknown flag exits 2"
  assert_contains "$out" "unknown option: --bogus" "unknown flag is named"
}

test_fixture_script_without_description_is_flagged_and_lib_excluded() {
  local out rc
  TEST_TMP="$(mktemp -d)"
  mkdir -p "$TEST_TMP/bin/lib"
  cp "$WF" "$TEST_TMP/bin/workflows"
  add_style_lib "$TEST_TMP/bin"
  printf '#!/usr/bin/env bash\n# shellcheck shell=bash\n# good — does a thing\n' >"$TEST_TMP/bin/good"
  printf '#!/usr/bin/env bash\necho hi\n' >"$TEST_TMP/bin/nodesc"
  printf '#!/usr/bin/env bash\n# hidden — in lib\n' >"$TEST_TMP/bin/lib/hidden"
  printf 'ELF' >"$TEST_TMP/bin/blob"
  chmod +x "$TEST_TMP"/bin/good "$TEST_TMP"/bin/nodesc "$TEST_TMP"/bin/lib/hidden "$TEST_TMP"/bin/blob
  capture_command out rc "$TEST_TMP/bin/workflows"
  assert_contains "$out" "good" "described script is listed"
  assert_contains "$out" "does a thing" "its description is shown"
  assert_contains "$out" "nodesc         (no description line)" "script without description is flagged"
  assert_not_contains "$out" "hidden" "bin/lib is excluded"
  assert_not_contains "$out" "blob" "non-script files are excluded"
}

test_follows_symlink() {
  local out rc
  TEST_TMP="$(mktemp -d)"
  ln -s "$WF" "$TEST_TMP/workflows"
  capture_command out rc "$TEST_TMP/workflows"
  assert_status 0 "$rc" "works through a symlink"
  assert_contains "$out" "  build " "finds the repo's bin/ through the symlink"
}

test_help_is_an_alias() {
  local out rc want
  capture_command out rc "$PROJECT_ROOT/bin/help"
  assert_status 0 "$rc" "bin/help exits 0"
  want="$("$WF")"
  assert_eq "$want" "$out" "bin/help prints what bin/workflows prints"
  capture_command out rc "$PROJECT_ROOT/bin/help" --bogus
  assert_status 2 "$rc" "alias passes arguments through"
}

run_tests "$@"
