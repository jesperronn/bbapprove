package style

import (
	"regexp"
	"strings"
	"testing"
)

func envOf(kv ...string) func(string) string {
	return func(k string) string {
		v := ""
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i] == k {
				v = kv[i+1]
			}
		}
		return v
	}
}

func TestEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		tty  bool
		want bool
	}{
		{"tty", nil, true, true},
		{"pipe", nil, false, false},
		{"force on a pipe", []string{"FORCE_COLOR", "1"}, false, true},
		{"NO_COLOR on a tty", []string{"NO_COLOR", "1"}, true, false},
		{"NO_COLOR wins over FORCE_COLOR", []string{"NO_COLOR", "1", "FORCE_COLOR", "1"}, true, false},
		{"empty NO_COLOR is ignored", []string{"NO_COLOR", ""}, true, true},
	}
	for _, c := range cases {
		if got := Enabled(envOf(c.env...), c.tty); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestPaintEverySGR(t *testing.T) {
	want := map[string]string{
		Title: "1", Heading: "1;36", Cmd: "1;35", Flag: "32", Val: "94", Env: "33", Path: "36", Dim: "2",
		OK: "32", Warn: "33", Fail: "1;31", Good: "1;32", Bad: "1;31", Err: "31", Version: "1",
		SrcFlag: "35", SrcEnv: "33", SrcFile: "32", SrcDef: "2",
	}
	if len(want) != len(Classes()) {
		t.Fatalf("test covers %d classes, package has %d", len(want), len(Classes()))
	}
	for class, code := range want {
		if got := New(true).Paint(class, "x"); got != "\x1b["+code+"mx\x1b[0m" {
			t.Errorf("%s: %q", class, got)
		}
		if got := New(false).Paint(class, "x"); got != "x" {
			t.Errorf("%s plain: %q", class, got)
		}
	}
	if got := New(true).Paint("nope", "x"); got != "x" {
		t.Errorf("unknown class: %q", got)
	}
	if got := New(true).Paint(Dim, ""); got != "" {
		t.Errorf("empty text: %q", got)
	}
}

func TestSource(t *testing.T) {
	c := New(true)
	for src, want := range map[string]string{
		"flag": "\x1b[35m[flag]\x1b[0m", "env": "\x1b[33m[env]\x1b[0m", "file": "\x1b[32m[file]\x1b[0m",
		"default": "\x1b[2m[default]\x1b[0m", "unset": "\x1b[2m[unset]\x1b[0m",
	} {
		if got := c.Source(src); got != want {
			t.Errorf("%s: %q", src, got)
		}
	}
	if got := New(false).Source("file"); got != "[file]" {
		t.Errorf("plain: %q", got)
	}
}

func TestRender(t *testing.T) {
	in := "run {cmd:doctor} {flag:--apply} {{x}} {val:a}}b}"
	if got := New(false).MustRender(in); got != "run doctor --apply {x} a}b" {
		t.Errorf("plain: %q", got)
	}
	want := "run \x1b[1;35mdoctor\x1b[0m \x1b[32m--apply\x1b[0m {x} \x1b[94ma}b\x1b[0m"
	if got := New(true).MustRender(in); got != want {
		t.Errorf("colour: %q", got)
	}
}

func TestRenderErrors(t *testing.T) {
	for in, want := range map[string]string{
		"{nope:x}":        "unknown class",
		"{flag:x":         "unterminated",
		"a } b":           "unbalanced",
		"{flag}":          "without class",
		"{h:{dim:x}}":     "nested",
		"{flag:--a} {dim": "without class",
	} {
		_, err := New(true).Render(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", in, err, want)
		}
		if Check(in) == nil {
			t.Errorf("Check(%q) accepted", in)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("MustRender did not panic")
		}
	}()
	New(false).MustRender("{nope:x}")
}

func TestEscape(t *testing.T) {
	if got := New(false).MustRender(Escape("a{b}c")); got != "a{b}c" {
		t.Errorf("%q", got)
	}
}

var sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// StripSGR is used by other packages' tests through copy; here we only prove it round-trips Render.
func TestStrippingColourEqualsPlain(t *testing.T) {
	in := "{h:Usage:} {cmd:doctor} [{flag:--offline}] {val:FILE} {env:NO_COLOR} {path:~/x} {dim:(note)} {{}}"
	if got := sgrRe.ReplaceAllString(New(true).MustRender(in), ""); got != New(false).MustRender(in) {
		t.Errorf("%q", got)
	}
}

func TestSprintfEscapesArguments(t *testing.T) {
	in := "ran {cmd:%s} at {path:%s} in %s steps"
	if got := New(false).Sprintf(in, "x{y}", "/a}b", 3); got != "ran x{y} at /a}b in 3 steps" {
		t.Errorf("plain: %q", got)
	}
	want := "ran \x1b[1;35mx{y}\x1b[0m at \x1b[36m/a}b\x1b[0m in 3 steps"
	if got := New(true).Sprintf(in, "x{y}", "/a}b", 3); got != want {
		t.Errorf("colour: %q", got)
	}
}

func TestSourcesTag(t *testing.T) {
	plain := Theme{}
	if got := plain.Sources("env", "file"); got != "[env, file]" {
		t.Errorf("plain: %q", got)
	}
	if got := plain.Sources("default"); got != plain.Source("default") {
		t.Errorf("one name must equal Source: %q", got)
	}
	on := New(true)
	if got := on.Sources("env", "file"); got != "\x1b[2m[\x1b[0m\x1b[33menv\x1b[0m\x1b[2m, \x1b[0m\x1b[32mfile\x1b[0m\x1b[2m]\x1b[0m" {
		t.Errorf("coloured: %q", got)
	}
}
