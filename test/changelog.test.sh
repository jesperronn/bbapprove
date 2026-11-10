#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
SCRIPT="$PROJECT_ROOT/bin/changelog"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() {
  [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"
  TEST_TMP=""
  return 0
}
trap 'cleanup_test_tmp' EXIT

# A tiny repository with fixed dates: tag v1.0.0, then a feat and a fix and a chore.
make_repo() {
  TEST_TMP="$(mktemp -d)"
  local r="$TEST_TMP/repo" n=0
  git init -q "$r"
  git -C "$r" config user.name "Test User"
  git -C "$r" config user.email "test@example.com"
  git -C "$r" config commit.gpgsign false
  git -C "$r" config tag.gpgsign false
  commit() {
    n=$((n + 1))
    GIT_AUTHOR_DATE="2026-11-0${n}T10:00:00Z" GIT_COMMITTER_DATE="2026-11-0${n}T10:00:00Z" \
      git -C "$r" commit -q --allow-empty -m "$1"
  }
  commit "chore: start"
  git -C "$r" tag -a v1.0.0 -m v1.0.0
  commit "feat(tui): add a key"
  commit "fix: stop a crash"
  commit "chore: tidy"
  printf '# Changelog\n\n## [1.0.0] - 2026-11-01\n\n### Added\n- old\n' >"$r/CHANGELOG.md"
  REPO_DIR="$r"
}

test_wrapper_runs_go_from_the_repo_root() {
  TEST_TMP="$(mktemp -d)"
  mkdir "$TEST_TMP/stubs"
  cat >"$TEST_TMP/stubs/go" <<'STUB'
#!/usr/bin/env bash
# stub go: "go build -o OUT ./cmd/changelog" writes a tool that reports where and how it ran
echo "go $*" >>"$STUB_LOG"
cat >"$3" <<'TOOL'
#!/usr/bin/env bash
echo "cwd=$(pwd -P)"
echo "args=$*"
exit 3
TOOL
chmod +x "$3"
STUB
  chmod +x "$TEST_TMP/stubs/go"
  local out rc=0
  unset CHANGELOG_BIN
  export STUB_LOG="$TEST_TMP/go.log"
  out="$(cd "$TEST_TMP" && PATH="$TEST_TMP/stubs:$PATH" "$SCRIPT" next --repo x)" || rc=$?
  assert_status 3 "$rc" "the tool's exit code is passed through"
  assert_contains "$(<"$STUB_LOG")" "go build -o" "the tool is built, not go-run"
  assert_contains "$out" "cwd=$PROJECT_ROOT" "go runs in the repo root"
  assert_contains "$out" "args=next --repo x" "arguments are passed through"
}

test_missing_go_exits_127() {
  TEST_TMP="$(mktemp -d)"
  mkdir "$TEST_TMP/bin"
  ln -s "$(command -v bash)" "$TEST_TMP/bin/bash"
  local rc=0 out
  out="$(PATH="$TEST_TMP/bin" "$TEST_TMP/bin/bash" "$SCRIPT" next 2>&1)" || rc=$?
  assert_status 127 "$rc" "no go on PATH"
  assert_contains "$out" "needs go" "hint printed"
}

test_real_run_on_a_fixture_repo() {
  make_repo
  local out rc=0
  (cd "$PROJECT_ROOT" && go build -o "$TEST_TMP/changelog" ./cmd/changelog)
  export CHANGELOG_BIN="$TEST_TMP/changelog"
  out="$("$SCRIPT" next --repo "$REPO_DIR")"
  assert_eq "v1.1.0" "$out" "next version"
  "$SCRIPT" update --repo "$REPO_DIR" --date 2030-01-02 >/dev/null
  out="$(<"$REPO_DIR/CHANGELOG.md")"
  assert_contains "$out" "## [1.1.0] - 2030-01-02" "update wrote the heading"
  assert_contains "$out" "- **tui:** add a key" "feat entry under Added"
  assert_contains "$out" "- stop a crash" "fix entry under Fixed"
  assert_not_contains "$out" "tidy" "chore not listed"
  assert_contains "$out" "- old" "existing entries kept"
  "$SCRIPT" update --repo "$REPO_DIR" --date 2030-01-02 >/dev/null 2>&1 || rc=$?
  assert_status 1 "$rc" "second update refuses"
  rc=0
  "$SCRIPT" due --repo "$REPO_DIR" --now 2026-11-04T11:00:00Z >/dev/null || rc=$?
  assert_status 4 "$rc" "one hour after the last commit is not yet due"
  rc=0
  out="$("$SCRIPT" due --repo "$REPO_DIR" --now 2026-11-04T12:00:00Z)" || rc=$?
  assert_status 0 "$rc" "two hours after the last commit is due"
  assert_eq "0s" "$out" "nothing left"
}

run_tests "$@"
