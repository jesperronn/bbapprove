package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const logOut = "b2\x1ffeat(tui): add keys\x1f\x1e" + "b1\x1ffix: stop crash\x1f\x1e"

func fakeGit(tag, log string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		switch args[0] {
		case "describe":
			if tag == "" {
				return "", errors.New("fatal: No names found")
			}
			return tag + "\n", nil
		case "log":
			if args[1] == "-1" {
				return "2026-11-09T10:00:00Z\n", nil
			}
			return log, nil
		}
		return "", errors.New("unexpected git call")
	}
}

func do(t *testing.T, tag, log string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb strings.Builder
	e := env{out: &out, err: &errb, run: fakeGit(tag, log),
		now: func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) }}
	code := e.main(args)
	return code, out.String(), errb.String()
}

func TestNextAndRender(t *testing.T) {
	code, out, _ := do(t, "v1.0.0", logOut, "next")
	if code != 0 || out != "v1.1.0\n" {
		t.Errorf("next: %d %q", code, out)
	}
	code, out, _ = do(t, "", logOut, "next")
	if code != 0 || out != "v0.1.0\n" {
		t.Errorf("next without tag: %d %q", code, out)
	}
	code, out, _ = do(t, "v1.0.0", logOut, "render", "--date", "2030-01-02")
	want := "## [1.1.0] - 2030-01-02\n\n### Added\n- **tui:** add keys\n\n### Fixed\n- stop crash\n"
	if code != 0 || out != want {
		t.Errorf("render: %d %q", code, out)
	}
	code, out, _ = do(t, "v1.0.0", logOut, "render", "--version", "v3.0.0")
	if code != 0 || !strings.HasPrefix(out, "## [3.0.0] - 2026-11-09\n") {
		t.Errorf("render --version: %d %q", code, out)
	}
}

func TestNothingReleasable(t *testing.T) {
	for _, sub := range []string{"next", "render", "update"} {
		code, out, errs := do(t, "v1.0.0", "c1\x1fchore: tidy\x1f\x1e", sub)
		if code != 3 || out != "" || !strings.Contains(errs, "nothing releasable since v1.0.0") {
			t.Errorf("%s: %d %q %q", sub, code, out, errs)
		}
	}
}

func TestUpdate(t *testing.T) {
	f := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(f, []byte("# Changelog\n\n## [1.0.0] - 2026-01-01\n\n### Added\n- old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := do(t, "v1.0.0", logOut, "update", "--file", f)
	if code != 0 {
		t.Fatalf("update: %d %s", code, errs)
	}
	b, _ := os.ReadFile(f)
	if !strings.Contains(string(b), "## [1.1.0] - 2026-11-09\n\n### Added\n- **tui:** add keys") || !strings.Contains(string(b), "## [1.0.0]") {
		t.Errorf("file:\n%s", b)
	}
	code, _, errs = do(t, "v1.0.0", logOut, "update", "--file", f)
	if code != 1 || !strings.Contains(errs, "already exists") {
		t.Errorf("second update: %d %q", code, errs)
	}
}

func TestDue(t *testing.T) {
	code, out, _ := do(t, "", "", "due", "--last-activity", "2026-11-09T11:00:00Z", "--now", "2026-11-09T12:00:00Z")
	if code != 4 || out != "1h0m0s\n" {
		t.Errorf("not due: %d %q", code, out)
	}
	code, out, _ = do(t, "", "", "due", "--last-activity", "2026-11-09T10:00:00Z", "--now", "2026-11-09T12:00:00Z")
	if code != 0 || out != "0s\n" {
		t.Errorf("due at grace: %d %q", code, out)
	}
	code, _, _ = do(t, "", "", "due", "--grace", "3h") // last activity from HEAD (10:00), now 12:00
	if code != 4 {
		t.Errorf("custom grace: %d", code)
	}
	code, _, _ = do(t, "", "", "due", "--grace", "abc")
	if code != 2 {
		t.Errorf("bad grace: %d", code)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := do(t, "", ""); code != 2 {
		t.Errorf("no args: %d", code)
	}
	if code, _, _ := do(t, "", "", "bogus"); code != 2 {
		t.Errorf("bogus: %d", code)
	}
	if code, _, _ := do(t, "", "", "next", "--bogus"); code != 2 {
		t.Errorf("bad flag: %d", code)
	}
	if code, _, _ := do(t, "", "", "next", "stray"); code != 2 {
		t.Errorf("stray arg: %d", code)
	}
	if code, _, _ := do(t, "", "", "render", "--date", "yesterday"); code != 2 {
		t.Errorf("bad date: %d", code)
	}
	for _, sub := range []string{"next", "render", "update", "due"} {
		if code, _, _ := do(t, "", "", sub, "-h"); code != 0 {
			t.Errorf("%s -h: %d", sub, code)
		}
	}
	if code, out, _ := do(t, "", "", "--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("--help: %d", code)
	}
}

func helpWith(t *testing.T, env map[string]string, args ...string) (string, string) {
	t.Helper()
	var out, errb strings.Builder
	e := env2(&out, &errb, env)
	e.main(args)
	return out.String(), errb.String()
}

func env2(out, errb *strings.Builder, vars map[string]string) env {
	return env{out: out, err: errb, now: time.Now, look: func(k string) string { return vars[k] }}
}

func TestHelpColour(t *testing.T) {
	const sgr = "\x1b["
	for _, args := range [][]string{{"--help"}, {"next", "-h"}} {
		on, onErr := helpWith(t, map[string]string{"FORCE_COLOR": "1"}, args...)
		if !strings.Contains(on+onErr, sgr) {
			t.Errorf("%v with FORCE_COLOR: no SGR escape in %q %q", args, on, onErr)
		}
		off, offErr := helpWith(t, map[string]string{"FORCE_COLOR": "1", "NO_COLOR": "1"}, args...)
		if strings.Contains(off+offErr, sgr) {
			t.Errorf("%v with NO_COLOR: SGR escape in %q %q", args, off, offErr)
		}
		plain, plainErr := helpWith(t, nil, args...)
		if plain != off || plainErr != offErr {
			t.Errorf("%v: plain output differs from NO_COLOR output", args)
		}
	}
	if plain, _ := helpWith(t, nil, "--help"); plain != topUsage+topUsageDetail {
		t.Errorf("plain help changed: %q", plain)
	}
}
