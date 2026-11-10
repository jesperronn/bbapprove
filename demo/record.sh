#!/usr/bin/env bash
# shellcheck shell=bash
# record — record the demo TUI on fictional PRs: drives it in a detached tmux session, writes an asciicast
# (demo/demo.cast) from timed screen captures, and renders it to demo/demo.svg with demo/cast2svg (Go) and freeze.
#
# Usage: demo/record.sh            (normally run as bin/demo record)
#   Needs tmux, freeze (BBAPPROVE_FREEZE or PATH) and go, which builds demo/cast2svg. Nothing is downloaded.
#   Env: DEMO_OUT_DIR    output directory (default: demo/)
#        DEMO_TICK_MS    milliseconds per capture (default 250). The cast's timestamps count this per
#                        capture, not the wall clock, so the animation timing is the same on every run
#        DEMO_TICK_SLEEP seconds to sleep between captures (default 0.25; the tests use 0)
#        DEMO_READY_TIMEOUT  seconds to wait for the TUI to draw its first screen (default 60)
#        DEMO_PREBUILD=0 skips building bbapprove first (the tests use it, with stubs)
#        DEMO_VERSION    the version shown in the recording (default: the newest release tag; see demo/build.sh)
# Progress goes to stderr. A frame that shows the checkout's bbapprove-go binary, $HOME or the temp directory
# fails the recording: nothing is written, so no local path can reach the animation.
# Exit: 0 recorded, 1 the TUI did not come up, the render failed or a frame leaked a path, 127 a required tool is
# not installed, 130 interrupted (the tmux session and the temp directory are removed).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
OUT="${DEMO_OUT_DIR:-$ROOT/demo}"
TICK_MS="${DEMO_TICK_MS:-250}"
TICK_SLEEP="${DEMO_TICK_SLEEP:-0.25}"
READY_TIMEOUT="${DEMO_READY_TIMEOUT:-60}"
COLS=100
FREEZE_VERSION=v0.2.2
ROWS=28
SOCK="bbapprove-demo-rec-$$"
SESSION=rec

# The scripted session: each step sends one key (empty: none) and then records for the given milliseconds.
# Keys are the TUI's own: j moves down, a approves and merges the cursor row, q quits. The extended help (?) is
# left out: its wall of text does not fit the pane. Row 102 (http-kit) is the one eligible row below the cursor,
# so the approval shows a merge.
STEPS=(
  "" 2000  # the list as it opens
  "j" 1500 # cursor onto ACME/shop#102
  "a" 3000 # approve + merge; the row shows MERGED
  "q" 2500 # quit: the session summary stays on screen
)

fail() {
  echo "record: $*" >&2
  exit 1
}

# check_frame TEXT: fails when a frame shows a local path. The list covers the checkout's built binary, the home
# directory and the temp directory (the launcher's build line and the state file are the usual sources).
check_frame() {
  local text="$1" leak
  for leak in "/bin/bbapprove-go" "$HOME" "${TMPDIR:-/tmp}"; do
    if [[ -n "$leak" && "$leak" != / && "$text" == *"$leak"* ]]; then
      fail "a frame shows $leak; nothing was written"
    fi
  done
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "record: $1 is not installed; $2" >&2
    exit 127
  fi
}

STATE_DIR=""
cleanup() {
  tmux -L "$SOCK" kill-server >/dev/null 2>&1 || true
  [[ -n "$STATE_DIR" ]] && rm -rf "$STATE_DIR"
  return 0
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# json_string TEXT: TEXT as a JSON string literal (quotes, backslashes, ESC and the control characters a
# frame can contain). Plain bash, no python or sed, so the output does not depend on the locale.
json_string() {
  local s="$1" esc
  esc='\u001b'
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\x1b'/$esc}"
  s="${s//$'\t'/\\t}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\n'/\\n}"
  printf '"%s"' "$s"
}

# ms_to_seconds MS: "12.345", the asciicast time for MS milliseconds (integer arithmetic, no float).
ms_to_seconds() { printf '%d.%03d' $(($1 / 1000)) $(($1 % 1000)); }

# capture: the pane as text with its colours (SGR escapes), one line per row. The "Pane is dead" line that
# remain-on-exit adds after quit is dropped.
capture() {
  tmux -L "$SOCK" -f /dev/null capture-pane -p -e -t "$SESSION" 2>/dev/null | awk '!/^Pane is dead/' || true
}

# emit MS FRAME: one asciicast "o" event that clears the screen and draws FRAME, rows joined with CRLF.
emit() {
  local ms="$1" frame="$2" data
  frame="${frame//$'\n'/$'\r\n'}"
  data=$'\x1b[H\x1b[2J'"$frame"
  printf '[%s, "o", %s]\n' "$(ms_to_seconds "$ms")" "$(json_string "$data")" >>"$CAST_TMP"
}

# pause: wait one capture interval (none when DEMO_TICK_SLEEP=0, as in the tests).
pause() {
  if [[ "$TICK_SLEEP" != 0 ]]; then sleep "$TICK_SLEEP"; fi
}

need tmux "install tmux (it is not part of macOS; e.g. from your system's package manager)"
need go "install Go; it builds demo/cast2svg"
FREEZE="${BBAPPROVE_FREEZE:-}"
if [[ -z "$FREEZE" ]]; then FREEZE="$(command -v freeze 2>/dev/null || true)"; fi
if [[ -z "$FREEZE" || ! -x "$FREEZE" ]]; then
  echo "record: freeze is not installed (regenerate needs tmux, freeze and go); install the pinned version with: GOBIN=DIR go install github.com/charmbracelet/freeze@$FREEZE_VERSION, then put DIR on PATH or set BBAPPROVE_FREEZE" >&2
  exit 127
fi
mkdir -p "$OUT"

# Build bbapprove before the session starts: the launcher's build line would otherwise sit in the pane.
if [[ "${DEMO_PREBUILD:-1}" != 0 ]]; then
  echo "record: building bbapprove $("$ROOT/demo/build.sh") (a fixed version, so the footer does not change per commit)" >&2
fi

STATE_DIR="$(mktemp -d)"
CAST_TMP="$STATE_DIR/demo.cast"
STATE_FILE="$STATE_DIR/state"

# The TUI runs with stderr discarded: the hint bin/demo prints there would otherwise be drawn into the pane.
tmux -L "$SOCK" -f /dev/null new-session -d -s "$SESSION" -x "$COLS" -y "$ROWS" -c "$ROOT" \
  "clear; env TERM=xterm-256color BBAPPROVE_DEMO_STATE='$STATE_FILE' '$ROOT/bin/demo' run 2>/dev/null" ||
  fail "tmux could not start the demo session"
tmux -L "$SOCK" -f /dev/null set-option -t "$SESSION" status off >/dev/null
tmux -L "$SOCK" -f /dev/null set-window-option -t "$SESSION" remain-on-exit on >/dev/null

# Wait for the TUI's column header: the first build of bbapprove may take a while.
waited=0
until capture | grep -q 'STATUS'; do
  if [[ "$waited" -ge "$READY_TIMEOUT" ]]; then
    fail "the demo TUI did not draw within ${READY_TIMEOUT}s"
  fi
  sleep 1
  waited=$((waited + 1))
done

printf '{"version": 2, "width": %d, "height": %d, "env": {"TERM": "xterm-256color"}, "title": "bbapprove demo"}\n' \
  "$COLS" "$ROWS" >"$CAST_TMP"

total_ms=0
for ((i = 1; i < ${#STEPS[@]}; i += 2)); do total_ms=$((total_ms + STEPS[i])); done
echo "record: recording $((${#STEPS[@]} / 2)) steps, about $((total_ms / 1000)) s" >&2

now=0
last="$(capture)"
check_frame "$last"
emit 0 "$last"
for ((i = 0; i < ${#STEPS[@]}; i += 2)); do
  key="${STEPS[i]}"
  span="${STEPS[i + 1]}"
  if [[ -n "$key" ]]; then
    tmux -L "$SOCK" -f /dev/null send-keys -t "$SESSION" -l "$key"
  fi
  for ((tick = 0; tick < span / TICK_MS; tick++)); do
    pause
    now=$((now + TICK_MS))
    frame="$(capture)"
    if [[ "$frame" != "$last" ]]; then
      check_frame "$frame"
      emit "$now" "$frame"
      last="$frame"
    fi
  done
done

mv "$CAST_TMP" "$OUT/demo.cast"
echo "record: rendering $OUT/demo.svg from $(grep -c '"o"' "$OUT/demo.cast") frames" >&2

(cd "$ROOT" && go build -o "$STATE_DIR/cast2svg" ./demo/cast2svg) || fail "demo/cast2svg does not build"
"$STATE_DIR/cast2svg" --in "$OUT/demo.cast" --out "$OUT/demo.svg" --freeze "$FREEZE" || fail "cast2svg failed"

echo "record: wrote $OUT/demo.cast and $OUT/demo.svg ($(wc -c <"$OUT/demo.svg" | tr -d ' ' | awk '{printf "%d KB", ($1 + 1023) / 1024}'))" >&2
