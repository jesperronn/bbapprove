#!/usr/bin/env bash
# shellcheck shell=bash
# style — colour for the bin/ commands, with the palette and rule of internal/style (sourced, not run)
#
# Rule (as internal/style.Enabled): a non-empty NO_COLOR turns colour off and wins; otherwise it is on when the
# stream is a terminal or FORCE_COLOR is non-empty. Tests and pipes get plain text.

# The terminal state of stdout and stderr, taken once when this file is sourced. paint is usually called inside
# $(...), where stdout is a pipe, so a live `-t 1` there would always say "not a terminal".
STYLE_TTY_1=0 STYLE_TTY_2=0
if [[ -t 1 ]]; then STYLE_TTY_1=1; fi
if [[ -t 2 ]]; then STYLE_TTY_2=1; fi

# style_on FD: succeeds when colour is on for file descriptor FD (live check, for use outside $(...)).
style_on() {
  [[ -z "${NO_COLOR:-}" ]] || return 1
  [[ -t "$1" || -n "${FORCE_COLOR:-}" ]]
}

# style_on_cached FD: like style_on, but for 1 and 2 from the state taken at source time.
style_on_cached() {
  [[ -z "${NO_COLOR:-}" ]] || return 1
  [[ -n "${FORCE_COLOR:-}" ]] && return 0
  case "$1" in
    1) [[ "$STYLE_TTY_1" == 1 ]] ;;
    2) [[ "$STYLE_TTY_2" == 1 ]] ;;
    *) [[ -t "$1" ]] ;;
  esac
}

# paint CLASS FD TEXT: TEXT in the colour of CLASS (the SGR codes of internal/style) when colour is on for FD,
# else TEXT unchanged. An unknown CLASS leaves TEXT unchanged.
paint() {
  local class="$1" fd="$2" text="$3" sgr=""
  case "$class" in
    title) sgr=1 ;;
    heading) sgr="1;36" ;;
    cmd) sgr="1;35" ;;
    flag) sgr=32 ;;
    val) sgr=94 ;;
    env) sgr=33 ;;
    path) sgr=36 ;;
    dim) sgr=2 ;;
    ok) sgr=32 ;;
    warn) sgr=33 ;;
    fail) sgr="1;31" ;;
    err) sgr=31 ;;
  esac
  if [[ -n "$sgr" ]] && style_on_cached "$fd"; then
    printf '\033[%sm%s\033[0m' "$sgr" "$text"
  else
    printf '%s' "$text"
  fi
}

# style_help: a filter for usage text, stdin to stdout; colour is decided by stdout (so `usage >&2` follows
# stderr). Off, it is cat. On, it paints a leading "Label:" (heading), --long and -x flags (flag), bin/NAME and
# the first-column names of indented rows (cmd), and NAME_WITH_UNDERSCORE (env). Words such as pre-release
# are whole tokens and never match.
style_help() {
  if ! style_on 1; then
    cat
    return
  fi
  LC_ALL=C awk '
    function p(sgr, s) { return sprintf("\033[%sm%s\033[0m", sgr, s) }
    function core(tok,    lead, trail, c) {
      match(tok, /^[[(`"\047]*/); lead = RLENGTH
      c = substr(tok, lead + 1)
      match(c, /[])`"\047.,;:]*$/); trail = RLENGTH
      c = substr(c, 1, length(c) - trail)
      if (c == "") return tok
      if (c ~ /^--[a-z][a-z0-9-]*$/ || c ~ /^-[A-Za-z]$/) c = p("32", c)
      else if (c ~ /^bin\/[a-z][a-z-]*$/) c = p("1;35", c)
      else if (c ~ /^[A-Z][A-Z0-9]*_[A-Z0-9_*]*$/) c = p("33", c)
      return substr(tok, 1, lead) c substr(tok, length(tok) - trail + 1)
    }
    {
      line = $0; out = ""
      if (match(line, /^[A-Z][A-Za-z\/]*( [A-Za-z]+)?:( |$)/)) {
        n = RLENGTH; if (substr(line, n, 1) == " ") n--
        out = p("1;36", substr(line, 1, n)); line = substr(line, n + 1)
      } else if (match(line, /^  +[a-z][a-z-]*(   *[^ ]|$)/)) {
        s = line; match(s, /^  +/); a = RLENGTH
        match(s, /^  +[a-z][a-z-]*/); out = substr(s, 1, a) p("1;35", substr(s, a + 1, RLENGTH - a))
        line = substr(s, RLENGTH + 1)
      }
      while (match(line, /[^ ]+/)) {
        st = RSTART; ln = RLENGTH
        out = out substr(line, 1, st - 1) core(substr(line, st, ln))
        line = substr(line, st + ln)
      }
      print out line
    }'
}
