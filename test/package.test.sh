#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PACKAGE="$PROJECT_ROOT/bin/package"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() {
  [[ -z "$TEST_TMP" ]] || rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
trap 'cleanup_test_tmp' EXIT

# Fake repo: the real bin/package, a stand-in bin/build that writes a fake binary, and the files that get packed.
setup_repo() {
  TEST_TMP="$(mktemp -d)"
  REPO="$TEST_TMP/repo"
  OUT="$TEST_TMP/out"
  mkdir -p "$REPO/bin" "$REPO/adapters"
  link_script "$PACKAGE" "$REPO/bin/package"
  use_stubs "$REPO/bin" package-build=build
  printf '#!/usr/bin/env bash\n' >"$REPO/bin/bbapprove"
  printf '#!/usr/bin/env bash\n' >"$REPO/adapters/bitbucket"
  printf 'template\n' >"$REPO/bbapprove.config.template"
  printf 'readme\n' >"$REPO/README.md"
  printf 'changelog\n' >"$REPO/CHANGELOG.md"
  printf 'license\n' >"$REPO/LICENSE"
}

run_package() {
  capture_command PKG_OUT PKG_RC "$REPO/bin/package" "$@"
}

test_builds_five_archives_and_sums() {
  setup_repo
  run_package v9.9.9 --out "$OUT"
  assert_eq "$PKG_RC" "0" "package succeeds"
  local names
  names="$(cd "$OUT" && find . -type f | LC_ALL=C sort | tr '\n' ' ')"
  assert_eq "$names" "./SHA256SUMS ./bbapprove-v9.9.9-darwin-amd64.tar.gz ./bbapprove-v9.9.9-darwin-arm64.tar.gz ./bbapprove-v9.9.9-linux-amd64.tar.gz ./bbapprove-v9.9.9-linux-arm64.tar.gz ./bbapprove-v9.9.9-windows-amd64.zip " "archives and SHA256SUMS"
  assert_eq "$(wc -l <"$OUT/SHA256SUMS" | tr -d ' ')" "5" "SHA256SUMS lists every archive"
  local sums_ok=0
  (cd "$OUT" && shasum -a 256 -c SHA256SUMS >/dev/null 2>&1) && sums_ok=1
  assert_eq "$sums_ok" "1" "checksums verify"
  cleanup_test_tmp
}

test_windows_zip_layout() {
  setup_repo
  run_package v9.9.9 --out "$OUT" --only windows/amd64
  assert_eq "$PKG_RC" "0" "package succeeds"
  local listing
  listing="$(unzip -Z1 "$OUT/bbapprove-v9.9.9-windows-amd64.zip" | LC_ALL=C sort | tr '\n' ' ')"
  local d="bbapprove-v9.9.9-windows-amd64"
  assert_eq "$listing" "$d/ $d/CHANGELOG.md $d/LICENSE $d/README.md $d/bbapprove.config.template $d/bbapprove.exe " "one folder with exactly the five files"
  assert_eq "$(unzip -p "$OUT/bbapprove-v9.9.9-windows-amd64.zip" "$d/bbapprove.exe")" "fake windows/amd64 v9.9.9" "exe built for windows with the tag"
  cleanup_test_tmp
}

test_unix_tar_layout() {
  setup_repo
  run_package v9.9.9 --out "$OUT" --only linux/arm64
  assert_eq "$PKG_RC" "0" "package succeeds"
  local listing d="bbapprove-v9.9.9-linux-arm64"
  listing="$(tar -tzf "$OUT/$d.tar.gz" | LC_ALL=C sort | tr '\n' ' ')"
  assert_eq "$listing" "$d/ $d/CHANGELOG.md $d/LICENSE $d/README.md $d/adapters/ $d/adapters/bitbucket $d/bbapprove.config.template $d/bin/ $d/bin/bbapprove $d/bin/bbapprove-go " "unix layout is unchanged"
  assert_eq "$(tar -xzOf "$OUT/$d.tar.gz" "$d/bin/bbapprove-go")" "fake linux/arm64 v9.9.9" "binary built for the target"
  cleanup_test_tmp
}

test_only_selects_targets() {
  setup_repo
  run_package v9.9.9 --out "$OUT" --only windows/amd64 --only darwin/arm64
  assert_eq "$PKG_RC" "0" "package succeeds"
  local names
  names="$(cd "$OUT" && find . -type f | LC_ALL=C sort | tr '\n' ' ')"
  assert_eq "$names" "./SHA256SUMS ./bbapprove-v9.9.9-darwin-arm64.tar.gz ./bbapprove-v9.9.9-windows-amd64.zip " "only the selected archives"
  assert_eq "$(wc -l <"$OUT/SHA256SUMS" | tr -d ' ')" "2" "SHA256SUMS lists just those"
  cleanup_test_tmp
}

test_usage_errors_exit_2() {
  setup_repo
  run_package v9.9.9 --bogus
  assert_eq "$PKG_RC" "2" "unknown option exits 2"
  assert_contains "$PKG_OUT" "unknown option: --bogus" "message names the option"
  run_package v9.9.9 --only plan9/mips
  assert_eq "$PKG_RC" "2" "unknown target exits 2"
  run_package
  assert_eq "$PKG_RC" "2" "missing tag exits 2"
  cleanup_test_tmp
}

test_missing_zip_exits_127() {
  setup_repo
  make_tool_dir "$TEST_TMP/tools" tar shasum sha256sum gzip
  capture_command PKG_OUT PKG_RC env PATH="$TEST_TMP/tools" "$REPO/bin/package" v9.9.9 --out "$OUT" --only windows/amd64
  assert_eq "$PKG_RC" "127" "no zip exits 127"
  assert_contains "$PKG_OUT" "needs zip" "hint names the tool"
  cleanup_test_tmp
}

run_tests "$@"
