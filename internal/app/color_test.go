package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bbapprove/internal/style"
)

var sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripSGR(s string) string { return sgrRe.ReplaceAllString(s, "") }

// helpRun runs the CLI with a scratch HOME-less environment: no config, no token, no adapter.
func helpRun(t *testing.T, tty bool, env []string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	env = append([]string{"XDG_CONFIG_HOME=" + t.TempDir()}, env...)
	code = Run(context.Background(), Options{Args: args, Environ: env, RepoDir: t.TempDir(), Stdout: &so, Stderr: &se, IsTTY: tty, Version: "v1.2.3"})
	return code, so.String(), se.String()
}

func mustHave(t *testing.T, name, out string, wants map[string]string) {
	t.Helper()
	for k, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("%s %s: missing %q", name, k, w)
		}
	}
}

func TestHelpMarkupIsWellFormed(t *testing.T) {
	classRe := regexp.MustCompile(`\{([a-zA-Z_-]+):`)
	known := map[string]bool{}
	for _, c := range style.Classes() {
		known[c] = true
	}
	for name, m := range map[string]string{"top-level": helpTitle("v1") + usageMarkup, "doctor": doctorUsageMarkup} {
		if err := style.Check(m); err != nil {
			t.Errorf("%s help markup: %v", name, err)
		}
		for _, c := range classRe.FindAllStringSubmatch(m, -1) {
			if !known[c[1]] {
				t.Errorf("%s help: unknown class %q", name, c[1])
			}
		}
		if out := style.New(true).MustRender(m); strings.ContainsAny(sgrRe.ReplaceAllString(out, ""), "{}") {
			t.Errorf("%s help: raw braces leaked into the output", name)
		}
	}
	// a version with braces is data, not markup
	if got := usage("v{1}"); !strings.HasPrefix(got, "bbapprove v{1} — ") {
		t.Errorf("version not escaped: %q", got[:30])
	}
}

func TestColouredTopLevelHelp(t *testing.T) {
	code, out, errOut := helpRun(t, false, []string{"FORCE_COLOR=1"}, "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if !strings.HasPrefix(out, "\x1b[1mbbapprove v1.2.3 — approve and merge Renovate PRs on Bitbucket Server.\x1b[0m\n") {
		t.Errorf("title not bold: %q", out[:80])
	}
	mustHave(t, "help", out, map[string]string{
		"heading":  "\x1b[1;36mUsage:\x1b[0m",
		"heading2": "\x1b[1;36mEnv:\x1b[0m",
		"cmd":      "bbapprove \x1b[1;35mdoctor\x1b[0m [",
		"flag":     "[\x1b[32m--apply\x1b[0m]",
		"flag -i":  "\x1b[32m-i\x1b[0m opens an interactive TUI",
		"val":      "\x1b[32m--config\x1b[0m \x1b[94mFILE\x1b[0m",
		"env":      "\x1b[33mBBAPPROVE_JOBS\x1b[0m",
		"env2":     "\x1b[33mNO_COLOR\x1b[0m",
		"path":     "\x1b[36m~/.config/bbapprove/ignore\x1b[0m",
		"dim":      "\x1b[2m(no secrets)\x1b[0m",
	})
}

func TestColouredDoctorHelp(t *testing.T) {
	code, out, errOut := helpRun(t, true, nil, "doctor", "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	mustHave(t, "doctor help", out, map[string]string{
		"title":    "\x1b[1mbbapprove doctor — diagnose the installation and the configuration.\x1b[0m",
		"heading":  "\x1b[1;36mUsage:\x1b[0m",
		"cmd":      "bbapprove \x1b[1;35mdoctor\x1b[0m [",
		"flag":     "\x1b[32m--offline\x1b[0m",
		"val":      "\x1b[94mFILE\x1b[0m",
		"env":      "\x1b[33mBBAPPROVE_TOKEN\x1b[0m",
		"path":     "\x1b[36m$XDG_CONFIG_HOME/bbapprove/config.toml\x1b[0m",
		"ok":       "\x1b[32mok\x1b[0m / \x1b[32m✓\x1b[0m",
		"warn":     "\x1b[33mwarn\x1b[0m / \x1b[33m!\x1b[0m",
		"fail":     "\x1b[1;31mFAIL\x1b[0m / \x1b[1;31m✗\x1b[0m",
		"flag src": "\x1b[35m[flag]\x1b[0m",
		"env src":  "\x1b[33m[env]\x1b[0m",
		"file src": "\x1b[32m[file]\x1b[0m",
		"def src":  "\x1b[2m[default]\x1b[0m",
	})
}

func TestDoctorHelpNeedsNothing(t *testing.T) {
	top := usage("v1.2.3")
	for _, args := range [][]string{{"doctor", "--help"}, {"doctor", "-h"}, {"help", "doctor"}} {
		code, out, errOut := helpRun(t, false, []string{"NO_COLOR=1", "BBAPPROVE_ADAPTER=/nonexistent"}, args...)
		if code != 0 || errOut != "" || out != doctorUsage() || out == top {
			t.Errorf("%v: code %d err %q out %.40q", args, code, errOut, out)
		}
		if strings.Contains(out, "\x1b") || !strings.Contains(out, "Exit: 0 all good") {
			t.Errorf("%v: bad doctor help", args)
		}
	}
	for _, args := range [][]string{{"--help"}, {"help"}, {"--help", "doctor"}} {
		if code, out, _ := helpRun(t, false, nil, args...); code != 0 || out != top {
			t.Errorf("%v: expected the top-level help (code %d)", args, code)
		}
	}
}

func TestHelpColourSwitches(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"doctor", "--help"}, {"help", "doctor"}} {
		if _, out, _ := helpRun(t, false, nil, args...); strings.Contains(out, "\x1b") {
			t.Errorf("%v: escapes on a pipe", args)
		}
		if _, out, _ := helpRun(t, true, []string{"NO_COLOR=1", "FORCE_COLOR=1"}, args...); strings.Contains(out, "\x1b") {
			t.Errorf("%v: NO_COLOR must win over FORCE_COLOR", args)
		}
		if _, out, _ := helpRun(t, true, nil, args...); !strings.Contains(out, "\x1b[1;36m") {
			t.Errorf("%v: a terminal gets colour", args)
		}
	}
}

func TestStrippedColourEqualsPlainHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"doctor", "--help"}} {
		_, col, _ := helpRun(t, true, nil, args...)
		_, plain, _ := helpRun(t, false, nil, args...)
		if stripSGR(col) != plain || col == plain {
			t.Errorf("%v: stripped colour differs from plain", args)
		}
	}
}

// printConfigTTY runs --print-config on a scratch config and returns stdout with the scratch dir masked.
func printConfigTTY(t *testing.T, tty bool, env ...string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte("repository = \"https://bitbucket.example.com\"\nauthor_email = \"bot@example.com\"\n"), 0o600)
	var so, se bytes.Buffer
	code := Run(context.Background(), Options{
		Args:    []string{"--print-config", "--config", cfg},
		Environ: append([]string{"XDG_CONFIG_HOME=" + dir, "BBAPPROVE_UPDATE_REPO=example/repo"}, env...),
		RepoDir: dir, Stdout: &so, Stderr: &se, IsTTY: tty,
	})
	if code != 0 {
		t.Fatalf("code %d: %s", code, se.String())
	}
	return strings.ReplaceAll(so.String(), dir, "$DIR")
}

func TestColouredPrintConfig(t *testing.T) {
	out := printConfigTTY(t, false, "FORCE_COLOR=1")
	mustHave(t, "print-config", out, map[string]string{
		"label":       "\x1b[2mconfig file:\x1b[0m",
		"path":        "\x1b[36m$DIR/config.toml\x1b[0m",
		"setting":     "\x1b[2mrepository:\x1b[0m   ",
		"value":       "https://bitbucket.example.com  \x1b[32m[file]\x1b[0m",
		"src env":     "example/repo  \x1b[33m[env]\x1b[0m",
		"src default": "bitbucket  \x1b[2m[default]\x1b[0m",
		"disallow":    "\x1b[2mdisallow:\x1b[0m",
	})
	if strings.Contains(printConfigTTY(t, false), "\x1b") {
		t.Error("escapes on a pipe")
	}
	if strings.Contains(printConfigTTY(t, true, "NO_COLOR=1", "FORCE_COLOR=1"), "\x1b") {
		t.Error("NO_COLOR must win over FORCE_COLOR")
	}
	if got, want := stripSGR(printConfigTTY(t, true)), printConfigTTY(t, false); got != want {
		t.Errorf("stripped colour differs from plain:\n%s\n--\n%s", got, want)
	}
}

func TestColouredPrintConfigWithoutFile(t *testing.T) {
	var col, plain bytes.Buffer
	for tty, w := range map[bool]*bytes.Buffer{true: &col, false: &plain} {
		dir := t.TempDir()
		Run(context.Background(), Options{Args: []string{"--print-config"}, Environ: []string{"XDG_CONFIG_HOME=" + dir}, RepoDir: dir, Stdout: w, Stderr: &bytes.Buffer{}, IsTTY: tty})
		s := strings.ReplaceAll(w.String(), dir, "$DIR")
		w.Reset()
		w.WriteString(s)
	}
	if stripSGR(col.String()) != plain.String() || !strings.Contains(col.String(), "\x1b[2m[unset]\x1b[0m") {
		t.Errorf("plain:\n%s\ncolour:\n%s", plain.String(), col.String())
	}
}
