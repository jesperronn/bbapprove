// Package style is the one place that decides whether output is coloured and which colour each kind of
// token gets. Help, doctor and --print-config all go through it so they cannot drift apart.
//
// Only the standard 16 ANSI colours are used, so the result follows the user's terminal theme.
package style

import (
	"fmt"
	"sort"
	"strings"
)

const reset = "\x1b[0m"

// Token classes and the SGR parameters they map to.
const (
	Title   = "title"       // first help line
	Heading = "h"           // Usage:, section names
	Cmd     = "cmd"         // subcommand names
	Flag    = "flag"        // --apply, -i
	Val     = "val"         // placeholders: FILE, PR_URL, <tag>
	Env     = "env"         // environment variable names
	Path    = "path"        // file paths and URLs
	Dim     = "dim"         // notes, labels, defaults, secondary text
	OK      = "ok"          // ok, a check mark
	Warn    = "warn"        // warn, !
	Fail    = "fail"        // FAIL, a cross
	Good    = "good"        // "all good"
	Bad     = "bad"         // "N problem(s) found"
	Err     = "err"         // could not check
	Version = "ver"         // v1.2.3
	SrcFlag = "src-flag"    // [flag]
	SrcEnv  = "src-env"     // [env]
	SrcFile = "src-file"    // [file]
	SrcDef  = "src-default" // [default]
)

var sgr = map[string]string{
	Title:   "1",
	Heading: "1;36",
	Cmd:     "1;35",
	Flag:    "32",
	Val:     "94",
	Env:     "33",
	Path:    "36",
	Dim:     "2",
	OK:      "32",
	Warn:    "33",
	Fail:    "1;31",
	Good:    "1;32",
	Bad:     "1;31",
	Err:     "31",
	Version: "1",
	SrcFlag: "35",
	SrcEnv:  "33",
	SrcFile: "32",
	SrcDef:  "2",
}

// Classes lists every known class, sorted.
func Classes() []string {
	out := make([]string, 0, len(sgr))
	for k := range sgr {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Enabled is the single colour rule: NO_COLOR (non-empty) disables and wins over everything; otherwise
// colour is on for a terminal or when FORCE_COLOR is non-empty. env looks up one variable.
func Enabled(env func(string) string, isTTY bool) bool {
	if env("NO_COLOR") != "" {
		return false
	}
	return isTTY || env("FORCE_COLOR") != ""
}

// Theme paints tokens. The zero value paints nothing (identity), so plain output is the default.
type Theme struct{ On bool }

// New returns a Theme that colours when on is true.
func New(on bool) Theme { return Theme{On: on} }

// Paint wraps text in the class's SGR sequence and a reset. Without colour, or for an empty text or an
// unknown class, it returns text unchanged.
func (t Theme) Paint(class, text string) string {
	code, ok := sgr[class]
	if !t.On || !ok || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + reset
}

// Source paints a "[source]" tag by where a setting came from; anything unknown (e.g. "unset") is dim.
func (t Theme) Source(source string) string {
	class := Dim
	switch source {
	case "flag":
		class = SrcFlag
	case "env":
		class = SrcEnv
	case "file":
		class = SrcFile
	case "default":
		class = SrcDef
	}
	return t.Paint(class, "["+source+"]")
}

// Escape makes arbitrary text safe to embed in markup.
func Escape(s string) string {
	return strings.NewReplacer("{", "{{", "}", "}}").Replace(s)
}

// Render turns markup into text. Markup is plain text plus {class:text} tags; {{ and }} are literal braces.
// Tags do not nest. Without colour the tags are stripped, so the result is the plain text. Unknown
// classes, nested or unterminated tags and stray braces are errors.
func (t Theme) Render(markup string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(markup); {
		c := markup[i]
		switch {
		case c == '{' && i+1 < len(markup) && markup[i+1] == '{':
			b.WriteByte('{')
			i += 2
		case c == '}' && i+1 < len(markup) && markup[i+1] == '}':
			b.WriteByte('}')
			i += 2
		case c == '}':
			return "", fmt.Errorf("style: unbalanced '}' at offset %d", i)
		case c == '{':
			colon := strings.IndexByte(markup[i:], ':')
			if colon < 0 {
				return "", fmt.Errorf("style: '{' without class at offset %d", i)
			}
			class := markup[i+1 : i+colon]
			if _, ok := sgr[class]; !ok {
				return "", fmt.Errorf("style: unknown class %q at offset %d", class, i)
			}
			j := i + colon + 1
			var text strings.Builder
			closed := false
			for j < len(markup) {
				d := markup[j]
				if d == '{' && j+1 < len(markup) && markup[j+1] == '{' {
					text.WriteByte('{')
					j += 2
				} else if d == '}' && j+1 < len(markup) && markup[j+1] == '}' {
					text.WriteByte('}')
					j += 2
				} else if d == '{' {
					return "", fmt.Errorf("style: nested tag inside {%s:...} at offset %d", class, j)
				} else if d == '}' {
					closed = true
					j++
					break
				} else {
					text.WriteByte(d)
					j++
				}
			}
			if !closed {
				return "", fmt.Errorf("style: unterminated {%s:...} at offset %d", class, i)
			}
			b.WriteString(t.Paint(class, text.String()))
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), nil
}

// MustRender is Render for static markup held in source; it panics on malformed markup (a unit test
// checks every static text, so this never fires at run time).
func (t Theme) MustRender(markup string) string {
	s, err := t.Render(markup)
	if err != nil {
		panic(err)
	}
	return s
}

// Check validates markup without rendering colours.
func Check(markup string) error {
	_, err := Theme{}.Render(markup)
	return err
}
