package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"bbapprove/internal/style"
)

func TestEmbeddedTemplateMatchesRoot(t *testing.T) {
	root, err := os.ReadFile(filepath.Join("..", "..", TemplateName))
	if err != nil {
		t.Fatal(err)
	}
	if string(root) != Template {
		t.Fatalf("internal/config/%s is out of date with the repo-root %s; run: cp %s internal/config/",
			TemplateName, TemplateName, TemplateName)
	}
}

func TestSplitWords(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		bad  bool
	}{
		{"vim", []string{"vim"}, false},
		{"code -w", []string{"code", "-w"}, false},
		{`  emacsclient   -t  `, []string{"emacsclient", "-t"}, false},
		{`"/Applications/My Editor/ed" --wait`, []string{"/Applications/My Editor/ed", "--wait"}, false},
		{`ed 'a b' c\ d`, []string{"ed", "a b", "c d"}, false},
		{`vim; rm -rf x`, []string{"vim;", "rm", "-rf", "x"}, false}, // no shell: just words
		{`$(touch x)`, []string{"$(touch", "x)"}, false},
		{`ed "unterminated`, nil, true},
		{``, nil, false},
	} {
		got, err := SplitWords(tc.in)
		if (err != nil) != tc.bad || (!tc.bad && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("SplitWords(%q) = %q, %v; want %q (bad=%v)", tc.in, got, err, tc.want, tc.bad)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := DefaultPath(env(map[string]string{"XDG_CONFIG_HOME": "/x"})); got != filepath.Join("/x", "bbapprove", "config.toml") {
		t.Errorf("xdg: %q", got)
	}
	// without XDG_CONFIG_HOME Windows uses %AppData% (see internal/userconfig), not ~/.config
	if got := DefaultPath(env(map[string]string{"HOME": "/h"})); runtime.GOOS != "windows" && got != filepath.Join("/h", ".config", "bbapprove", "config.toml") {
		t.Errorf("home: %q", got)
	}
}

func TestWriteTemplate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "config.toml")
	if err := WriteTemplate(p, false); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	dst, _ := os.Stat(filepath.Dir(p))
	if runtime.GOOS != "windows" && (st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700) {
		t.Errorf("modes: file %v dir %v", st.Mode().Perm(), dst.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); string(b) != Template {
		t.Error("content differs from template")
	}
	os.WriteFile(p, []byte("mine"), 0o644)
	if err := WriteTemplate(p, false); !errors.Is(err, ErrExists) {
		t.Errorf("want ErrExists, got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "mine" {
		t.Error("existing file was overwritten")
	}
	if err := WriteTemplate(p, true); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != Template || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Error("force did not replace with 0600 template")
	}
}

func TestRootOfExecutable(t *testing.T) {
	j := filepath.Join
	for _, tc := range []struct{ name, exe, want string }{
		{"checkout layout", j("opt", "bbapprove", "bin", "bbapprove-go"), j("opt", "bbapprove")},
		{"Windows archive layout", j("C", "tools", "bbapprove-v1", "bbapprove.exe"), j("C", "tools", "bbapprove-v1")},
		{"bin in upper case", j("opt", "bbapprove", "BIN", "bbapprove-go"), j("opt", "bbapprove")},
		{"a directory merely called binary", j("opt", "binary", "bbapprove"), j("opt", "binary")},
		{"nested bin", j("opt", "bin", "x", "bbapprove"), j("opt", "bin", "x")},
	} {
		if got := RootOfExecutable(tc.exe); got != tc.want {
			t.Errorf("%s: RootOfExecutable(%q) = %q, want %q", tc.name, tc.exe, got, tc.want)
		}
	}
}

func TestCreateHintFor(t *testing.T) {
	unix := createHintFor("linux", style.Theme{}, "/h/.config/bbapprove/config.toml")
	if want := "mkdir -p /h/.config/bbapprove && cp bbapprove.config.template /h/.config/bbapprove/config.toml"; !strings.Contains(unix, want) {
		t.Errorf("unix hint = %q", unix)
	}
	win := createHintFor("windows", style.Theme{}, `C:\Users\me\AppData\Roaming\bbapprove\config.toml`)
	if want := `mkdir "C:\Users\me\AppData\Roaming\bbapprove" && copy bbapprove.config.template "C:\Users\me\AppData\Roaming\bbapprove\config.toml"`; !strings.Contains(win, want) {
		t.Errorf("windows hint = %q", win)
	}
	if strings.Contains(win, "mkdir -p") || strings.Contains(win, " cp ") {
		t.Errorf("windows hint uses Unix commands: %q", win)
	}
}
