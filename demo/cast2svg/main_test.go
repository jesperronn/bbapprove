package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const castText = `{"version": 2, "width": 20, "height": 3, "env": {"TERM": "xterm-256color"}}
[0.000, "o", "\u001b[H\u001b[2Jalpha one\r\nrow \u001b[32m2\u001b[0m"]
[1.500, "o", "\u001b[H\u001b[2Jalpha one\r\nrow \u001b[32m2\u001b[0m"]
[2.000, "o", "\u001b[H\u001b[2Jbeta \u001b]8;;https://example.com/x\u001b\\link\u001b]8;;\u001b\\\r\ngamma"]
[5.000, "o", "\u001b[H\u001b[2Jdelta only"]
[6.000, "o", "\u001b[H\u001b[2Jdelta only"]
`

// stubFreeze writes a freeze stand-in: it reads the frame on stdin and writes a small SVG with that text to -o.
func stubFreeze(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "freeze")
	if body == "" {
		body = `out=""; while [ $# -gt 0 ]; do [ "$1" = "-o" ] && out="$2"; shift; done
text=$(cat | tr '\033' '~' | tr '\n' '|')
printf '<?xml version="1.0"?>\n<svg width="840.00" height="600.00" xmlns="http://www.w3.org/2000/svg"><rect width="840.00" height="600.00"/><g clip-path="url(#m)"><text id="t">%s</text></g><svg><circle r="5"/></svg><defs><clipPath id="m"><rect width="1" height="1"/></clipPath></defs></svg>' "$text" > "$out"`
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func compose(t *testing.T, only int) (*Cast, string) {
	t.Helper()
	c, err := ParseCast(strings.NewReader(castText))
	if err != nil {
		t.Fatal(err)
	}
	svg, err := Compose(c, FreezeRenderer(stubFreeze(t, ""), DefaultFont, 840, 600), only)
	if err != nil {
		t.Fatal(err)
	}
	return c, string(svg)
}

func TestParseCastHeaderAndEvents(t *testing.T) {
	c, err := ParseCast(strings.NewReader(castText))
	if err != nil {
		t.Fatal(err)
	}
	if c.Cols != 20 || c.Rows != 3 {
		t.Errorf("size = %dx%d, want 20x3", c.Cols, c.Rows)
	}
	if c.End != 6 {
		t.Errorf("End = %v, want 6 (the last event, a repeat)", c.End)
	}
}

func TestParseCastRejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":      "",
		"not json":   "hello\n",
		"version 1":  `{"version": 1, "width": 1, "height": 1}` + "\n",
		"bad event":  `{"version": 2, "width": 1, "height": 1}` + "\n[1, 2]\n",
		"no outputs": `{"version": 2, "width": 1, "height": 1}` + "\n",
	} {
		if _, err := ParseCast(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestIdenticalConsecutiveFramesAreMerged(t *testing.T) {
	c, _ := compose(t, -1)
	if len(c.Frames) != 3 {
		t.Fatalf("frames = %d, want 3: %+v", len(c.Frames), c.Frames)
	}
	for i, at := range []float64{0, 2, 5} {
		if c.Frames[i].At != at {
			t.Errorf("frame %d At = %v, want %v", i, c.Frames[i].At, at)
		}
	}
}

func TestSanitizeStripsLinksAndCursorSequences(t *testing.T) {
	in := "\x1b[H\x1b[2J\x1b[?25l\x1b]8;;https://example.com/a\x1b\\go\x1b]8;;\x1b\\ \x1b[1;32mok\x1b[0m\x1b]8;;\x07\r\nz"
	got := Sanitize(in)
	want := "go \x1b[1;32mok\x1b[0m\nz"
	if got != want {
		t.Errorf("Sanitize = %q, want %q", got, want)
	}
}

func TestComposedSVGHasNoLinksOrCursorCodes(t *testing.T) {
	_, svg := compose(t, -1)
	for _, bad := range []string{"8;;", "example.com", "[2J", "[H"} {
		if strings.Contains(svg, bad) {
			t.Errorf("svg contains %q", bad)
		}
	}
	if !strings.Contains(svg, "beta ~[") && !strings.Contains(svg, "beta link|gamma") {
		t.Errorf("link text not kept:\n%s", svg)
	}
}

func TestTimingFollowsTheCastAndHoldsTheLastFrame(t *testing.T) {
	_, svg := compose(t, -1)
	total := 6 + FinalHold // 8.5 s
	for i := 0; i < 3; i++ {
		if !strings.Contains(svg, ".f"+string(rune('0'+i))+"{animation:f"+string(rune('0'+i))+"-show 8.500s step-end infinite}") {
			t.Errorf("frame %d has no 8.500s step-end infinite animation:\n%s", i, svg)
		}
	}
	// 2 s of 8.5 s = 23.5294%, 5 s = 58.8235%
	for _, want := range []string{
		"@keyframes f0-show{0%{opacity:1}23.5294%{opacity:0}}",
		"@keyframes f1-show{0%{opacity:0}23.5294%{opacity:1}58.8235%{opacity:0}}",
		"@keyframes f2-show{0%{opacity:0}58.8235%{opacity:1}}",
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("missing %q (total %v):\n%s", want, total, svg)
		}
	}
}

func TestSVGIsWellFormedAndKeepsOneChrome(t *testing.T) {
	_, svg := compose(t, -1)
	d := xml.NewDecoder(strings.NewReader(svg))
	for {
		_, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("not well-formed: %v\n%s", err, svg)
		}
	}
	if n := strings.Count(svg, "<circle"); n != 1 {
		t.Errorf("chrome appears %d times, want 1", n)
	}
	if n := strings.Count(svg, "<svg"); n != 2 { // the root and the chrome
		t.Errorf("<svg count = %d, want 2", n)
	}
	if !strings.Contains(svg, `viewBox="0 0 840.00 600.00"`) {
		t.Errorf("root has no viewBox of the frame size")
	}
}

func TestIDsAndClassesAreUniqueAcrossFrames(t *testing.T) {
	_, svg := compose(t, -1)
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllStringSubmatch(svg, -1) {
		if seen[m[1]] {
			t.Errorf("duplicate id %q", m[1])
		}
		seen[m[1]] = true
	}
	for _, id := range []string{"f0-t", "f1-t", "f2-t", "f0-frame", "f1-frame", "f2-frame"} {
		if !seen[id] {
			t.Errorf("missing id %q", id)
		}
	}
	for i := 0; i < 3; i++ {
		if n := strings.Count(svg, "@keyframes f"+string(rune('0'+i))+"-show"); n != 1 {
			t.Errorf("keyframes f%d defined %d times", i, n)
		}
	}
}

func TestFrameZeroIsVisibleByDefault(t *testing.T) {
	_, svg := compose(t, -1)
	if !strings.Contains(svg, ".frame{opacity:0}\n.f0{opacity:1}") {
		t.Errorf("only f0 should be visible without animation:\n%s", svg)
	}
}

func TestSingleFrameIsStaticAndHasNoAnimation(t *testing.T) {
	_, svg := compose(t, 2)
	if strings.Contains(svg, "@keyframes") || strings.Contains(svg, "<style") {
		t.Errorf("a single frame has no animation:\n%s", svg)
	}
	if !strings.Contains(svg, "delta only") || strings.Contains(svg, "beta") {
		t.Errorf("frame 2 should show only its own text:\n%s", svg)
	}
	c, _ := ParseCast(strings.NewReader(castText))
	if _, err := Compose(c, FreezeRenderer(stubFreeze(t, ""), DefaultFont, 1, 1), 9); err == nil {
		t.Error("frame 9 does not exist: want an error")
	}
}

func TestFreezeMissingNamesFreeze(t *testing.T) {
	dir := t.TempDir()
	cast := filepath.Join(dir, "c.cast")
	if err := os.WriteFile(cast, []byte(castText), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--in", cast, "--out", filepath.Join(dir, "o.svg"), "--freeze", filepath.Join(dir, "nope")}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "freeze is not installed") {
		t.Errorf("err = %v, want a message naming freeze", err)
	}
}

func TestFreezeFailureNamesFreeze(t *testing.T) {
	c, _ := ParseCast(strings.NewReader(castText))
	_, err := Compose(c, FreezeRenderer(stubFreeze(t, "echo boom >&2; exit 3"), DefaultFont, 1, 1), -1)
	if err == nil || !strings.Contains(err.Error(), "freeze") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want freeze's failure with its message", err)
	}
}

func TestRunWritesTheFile(t *testing.T) {
	dir := t.TempDir()
	cast, out := filepath.Join(dir, "c.cast"), filepath.Join(dir, "o.svg")
	if err := os.WriteFile(cast, []byte(castText), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := run([]string{"--in", cast, "--out", out, "--freeze", stubFreeze(t, "")}, &log); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(b), "@keyframes f2-show") {
		t.Errorf("output not written: %v", err)
	}
	if !strings.Contains(log.String(), "3 frames") {
		t.Errorf("log = %q", log.String())
	}
}
