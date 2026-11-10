#!/usr/bin/env bash
# shellcheck disable=SC1090,SC1091,SC2016

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
DEMO="$PROJECT_ROOT/bin/demo"

source "$PROJECT_ROOT/test/lib/bash_test.sh"

TEST_TMP=""
cleanup_test_tmp() { [[ -n "$TEST_TMP" ]] && rm -rf "$TEST_TMP"; TEST_TMP=""; return 0; }
trap 'cleanup_test_tmp' EXIT

# Fresh temp dir per test: the output goes there (DEMO_OUT_DIR), never into demo/. The stubs (tmux, go, freeze,
# and an npx that must never run) live in TEST_TMP/stubs; BASIC holds only the shell tools, so no real tmux, go
# or freeze is found.
setup_tmp() {
  cleanup_test_tmp
  TEST_TMP="$(mktemp -d)"
  export STUB_LOG="$TEST_TMP/calls.log"
  : >"$STUB_LOG"
  export DEMO_OUT_DIR="$TEST_TMP/out" DEMO_TICK_SLEEP=0 DEMO_TICK_MS=1500 DEMO_READY_TIMEOUT=2
  export BBAPPROVE_DEMO_STATE="$TEST_TMP/state"
  unset BBAPPROVE_FREEZE
  export DEMO_PREBUILD=0
  make_tool_dir "$TEST_TMP/basic" awk mv
}

# write_stubs: the recording stubs plus a go stub whose `build -o PATH` writes a cast2svg stub that logs its call
# and writes the --out file, and an npx that logs to NPX_LOG and fails: record must never call it.
write_stubs() {
  local dir="$TEST_TMP/stubs"
  use_stubs "$dir" recorder=tmux recorder=freeze
  export NPX_LOG="$TEST_TMP/npx.log"
  : >"$NPX_LOG"
  printf '%s\n' '#!/usr/bin/env bash' 'echo "npx $*" >>"$NPX_LOG"' 'exit 99' >"$dir/npx"
  printf '%s\n' '#!/usr/bin/env bash' \
    'echo "go $*" >>"$STUB_LOG"' \
    'out=""; while [[ $# -gt 0 ]]; do [[ "$1" == -o ]] && out="$2"; shift; done' \
    '[[ -n "$out" ]] || exit 0' \
    "printf '%s\\n' '#!/usr/bin/env bash' 'echo \"cast2svg \$*\" >>\"\$STUB_LOG\"' 'o=\"\"; while [[ \$# -gt 0 ]]; do [[ \"\$1\" == --out ]] && o=\"\$2\"; shift; done' 'echo \"<svg/>\" >\"\$o\"' >\"\$out\"" \
    'chmod +x "$out"' >"$dir/go"
  chmod +x "$dir/npx" "$dir/go"
}

# Canned pane: the TUI's header, a row, and the "Pane is dead" line that tmux adds after the demo quits.
canned_pane() {
  export STUB_TMUX_OUT=$'  \033[2mSTATUS       PR   TITLE\033[0m\n\033[48;5;237m\xe2\x80\xba              \033[32m\xe2\x9c\x93\033[39m ACME/shop#102\nPane is dead (status 0)'
}

test_record_without_tmux_hints_and_exits_127() {
  setup_tmp
  local out rc
  capture_command out rc env PATH="$TEST_TMP/basic" "$DEMO" record
  assert_status 127 "$rc" "record exits 127 when tmux is missing"
  assert_contains "$out" "tmux is not installed" "record names the missing tmux"
}

test_record_without_freeze_hints_and_exits_127() {
  setup_tmp
  write_stubs
  rm "$TEST_TMP/stubs/freeze"
  local out rc
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" record
  assert_status 127 "$rc" "record exits 127 when freeze is missing"
  assert_contains "$out" "freeze is not installed" "record names the missing freeze"
  assert_contains "$out" "go install github.com/charmbracelet/freeze@v0.2.2" "record prints the pinned install command"
}

test_record_without_go_hints_and_exits_127() {
  setup_tmp
  write_stubs
  rm "$TEST_TMP/stubs/go"
  local out rc
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" record
  assert_status 127 "$rc" "record exits 127 when go is missing"
  assert_contains "$out" "go is not installed" "record names the missing go"
}

test_record_help_says_tmux_freeze_and_go() {
  local out rc
  capture_command out rc "$DEMO" record --help
  assert_status 0 "$rc" "record --help exits 0"
  assert_contains "$out" "tmux (your system's package manager), freeze v0.2.2 (as for shots) and go" "record --help names its three tools"
  assert_not_contains "$out" "npx" "record --help does not mention npx"
}

test_shots_without_freeze_hints_and_exits_127() {
  setup_tmp
  local out rc
  capture_command out rc env -u BBAPPROVE_FREEZE PATH="$TEST_TMP/basic" "$DEMO" shots
  assert_status 127 "$rc" "shots exits 127 when freeze is missing"
  assert_contains "$out" "go install github.com/charmbracelet/freeze@v0.2.2" "shots prints the pinned install command"
}

test_record_drives_tmux_and_renders_with_cast2svg() {
  setup_tmp
  canned_pane
  write_stubs
  local out rc calls
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" record
  assert_status 0 "$rc" "record exits 0 when the stubs succeed"
  calls="$(cat "$STUB_LOG")"
  assert_contains "$calls" "new-session" "record starts a detached tmux session"
  assert_contains "$calls" "send-keys" "record sends the scripted keys"
  assert_contains "$calls" "capture-pane" "record captures the pane"
  assert_contains "$calls" "kill-server" "record stops its tmux server"
  assert_contains "$calls" "go build -o" "record builds cast2svg"
  assert_contains "$calls" "cast2svg --in $DEMO_OUT_DIR/demo.cast --out $DEMO_OUT_DIR/demo.svg --freeze $TEST_TMP/stubs/freeze" \
    "record renders with cast2svg and the freeze on PATH"
  assert_eq "" "$(cat "$NPX_LOG")" "record never calls npx"
  assert_eq "yes" "$([[ -f "$DEMO_OUT_DIR/demo.svg" ]] && echo yes || echo no)" "record writes demo.svg"
  assert_eq "yes" "$([[ -f "$DEMO_OUT_DIR/demo.cast" ]] && echo yes || echo no)" "record writes demo.cast"
}

test_record_cast_has_valid_header_and_events() {
  setup_tmp
  canned_pane
  write_stubs
  local out rc cast
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" record
  assert_status 0 "$rc" "record exits 0 for the cast check"
  cast="$DEMO_OUT_DIR/demo.cast"
  capture_command out rc python3 -I -c '
import json, sys
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
header = json.loads(lines[0])
assert header["version"] == 2 and header["width"] == 100 and header["height"] == 28, header
events = [json.loads(line) for line in lines[1:]]
assert events, "no events"
assert all(e[1] == "o" and isinstance(e[2], str) for e in events)
assert events[0][0] == 0, events[0]
print(len(events))
' "$cast"
  assert_status 0 "$rc" "the cast has a version 2 header and output events"
  assert_eq "1" "$out" "an unchanged pane gives one event"
  assert_not_contains "$(cat "$cast")" "Pane is dead" "the remain-on-exit line is not recorded"
}

test_record_leaves_the_demo_state_file_alone() {
  setup_tmp
  canned_pane
  write_stubs
  printf 'approved 101\n' >"$BBAPPROVE_DEMO_STATE"
  local out rc
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" record
  assert_status 0 "$rc" "record exits 0 with an existing state file"
  assert_eq "approved 101" "$(cat "$BBAPPROVE_DEMO_STATE")" "record uses its own state, not the demo's"
}

test_shots_runs_freeze_three_times() {
  setup_tmp
  use_stubs "$TEST_TMP/stubs" recorder=freeze
  local out rc calls
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" shots
  assert_status 0 "$rc" "shots exits with freeze's status"
  calls="$(cat "$STUB_LOG")"
  assert_eq "3" "$(grep -c 'freeze --execute' <<<"$calls")" "shots runs freeze three times"
  assert_contains "$calls" "-o $DEMO_OUT_DIR/shots/list.svg" "shots writes the list screen"
  assert_contains "$calls" "-o $DEMO_OUT_DIR/shots/doctor.svg" "shots writes the doctor screen"
  assert_contains "$calls" "-o $DEMO_OUT_DIR/shots/help.svg" "shots writes the help screen"
}

test_shots_applies_the_font_stack_and_the_doctor_path() {
  setup_tmp
  use_stubs "$TEST_TMP/stubs" recorder=freeze
  local out rc calls
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" shots
  assert_status 0 "$rc" "shots exits 0 for the font and path check"
  calls="$(cat "$STUB_LOG")"
  assert_contains "$calls" "--font.family JetBrains Mono, Menlo, Consolas, 'DejaVu Sans Mono', monospace" \
    "shots passes a font stack ending in a generic monospace"
  assert_contains "$calls" "env PATH=" "the doctor screen runs with its own PATH"
}

test_shipped_shots_have_a_monospace_fallback() {
  local bad files count
  files=("$PROJECT_ROOT"/demo/shots/*.svg)
  assert_eq "3" "${#files[@]}" "three screenshots are committed"
  count="$(grep -ho 'font-family="[^"]*"' "${files[@]}" | wc -l | tr -d ' ')"
  assert_eq "yes" "$([[ "$count" -gt 0 ]] && echo yes || echo no)" "the screenshots name a font family"
  bad="$(grep -ho 'font-family="[^"]*"' "${files[@]}" | grep -v 'monospace"' || true)"
  assert_eq "" "$bad" "every font-family in demo/shots ends with monospace"
}

test_record_reports_progress_on_stderr() {
  setup_tmp
  canned_pane
  write_stubs
  local out rc
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" record
  assert_status 0 "$rc" "record exits 0 for the progress check"
  assert_contains "$out" "recording 4 steps, about 9 s" "record says how long the recording takes"
  assert_contains "$out" "rendering $DEMO_OUT_DIR/demo.svg from 1 frames" "record says it is rendering"
  assert_contains "$out" "wrote $DEMO_OUT_DIR/demo.cast and $DEMO_OUT_DIR/demo.svg" "record says what it wrote"
}

test_record_refuses_a_frame_that_shows_a_local_path() {
  setup_tmp
  export STUB_TMUX_OUT=$'  STATUS       PR   TITLE\n/private/work/checkout/bin/bbapprove-go (v1.0.0)'
  write_stubs
  local out rc
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" record
  assert_status 1 "$rc" "record exits 1 when a frame shows a local path"
  assert_contains "$out" "a frame shows /bin/bbapprove-go" "record names the leaked path"
  assert_eq "no" "$([[ -e "$DEMO_OUT_DIR/demo.cast" ]] && echo yes || echo no)" "record writes nothing when a frame leaks"
  assert_not_contains "$(cat "$STUB_LOG")" "cast2svg" "record does not render a leaked recording"
}

test_record_cleans_up_when_terminated() {
  setup_tmp
  canned_pane
  write_stubs
  mkdir -p "$TEST_TMP/tmp"
  env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" TMPDIR="$TEST_TMP/tmp" DEMO_TICK_SLEEP=0.2 \
    bash "$PROJECT_ROOT/demo/record.sh" >/dev/null 2>&1 &
  local pid=$! rc=0
  sleep 0.6
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" || rc=$?
  assert_status 130 "$rc" "a terminated record exits 130"
  assert_contains "$(cat "$STUB_LOG")" "kill-server" "a terminated record stops its tmux session"
  assert_eq "0" "$(find "$TEST_TMP/tmp" -mindepth 1 | wc -l | tr -d ' ')" "a terminated record removes its temp files"
}

test_regenerate_records_then_shoots() {
  setup_tmp
  canned_pane
  write_stubs
  local out rc calls
  capture_command out rc env PATH="$TEST_TMP/stubs:$TEST_TMP/basic" "$DEMO" regenerate
  calls="$(cat "$STUB_LOG")"
  assert_contains "$calls" "new-session" "regenerate starts the record step"
  assert_contains "$calls" "freeze --execute" "regenerate then runs the shots step"
  assert_status 1 "$rc" "regenerate exits 1 when the stubs wrote no screenshots"
  assert_contains "$out" "regenerate: missing $DEMO_OUT_DIR/shots/list.svg" "regenerate names the file it did not get"
  assert_eq "" "$(cat "$NPX_LOG")" "regenerate never calls npx"
}

# demo/build.sh in a scratch tree whose bin/build only logs the version it was given
test_build_embeds_a_fixed_version() {
  setup_tmp
  mkdir -p "$TEST_TMP/tree/demo" "$TEST_TMP/tree/bin"
  cp "$PROJECT_ROOT/demo/build.sh" "$TEST_TMP/tree/demo/build.sh"
  printf '%s\n' '#!/usr/bin/env bash' 'echo "version=$BBAPPROVE_VERSION" >>"$STUB_LOG"' >"$TEST_TMP/tree/bin/build"
  chmod +x "$TEST_TMP/tree/bin/build"
  local out rc
  capture_command out rc env DEMO_VERSION=v9.9.9 "$TEST_TMP/tree/demo/build.sh"
  assert_status 0 "$rc" "build.sh succeeds"
  assert_eq "v9.9.9" "$out" "build.sh prints the version it built"
  assert_contains "$(cat "$STUB_LOG")" "version=v9.9.9" "bin/build gets DEMO_VERSION as BBAPPROVE_VERSION"
  capture_command out rc "$TEST_TMP/tree/demo/build.sh"
  assert_eq "dev" "$out" "without a release tag the version is dev"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 3' >"$TEST_TMP/tree/bin/build"
  capture_command out rc env DEMO_VERSION=v9.9.9 "$TEST_TMP/tree/demo/build.sh"
  assert_status 1 "$rc" "build.sh exits 1 when bin/build fails"
  assert_contains "$out" "bin/build failed" "build.sh names the failing step"
}

run_tests "$@"
