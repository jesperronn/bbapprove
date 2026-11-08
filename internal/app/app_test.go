package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bot = "renovate-bot@example.com"

const listJSON = `[
 {"url":"http://h/projects/P/repos/a/pull-requests/1","number":1,"project":"P","repo":"a","title":"chore(deps): update a","author":{"email":"` + bot + `"},"headBranch":"renovate/a","build":"passed","qualityGate":"passed","approvals":1,"approvedByMe":true},
 {"url":"http://h/projects/P/repos/b/pull-requests/2","number":2,"project":"P","repo":"b","title":"chore(deps): update another-library","author":{"email":"` + bot + `"},"headBranch":"renovate/k"},
 {"url":"http://h/projects/P/repos/c/pull-requests/3","number":3,"project":"P","repo":"c","title":"chore(deps): update c","author":{"email":"` + bot + `"},"headBranch":"renovate/c","commentCount":2},
 {"url":"http://h/projects/P/repos/d/pull-requests/4","number":4,"project":"P","repo":"d","title":"chore(deps): update d","author":{"email":"` + bot + `"},"headBranch":"renovate/d"}
]`

// fake writes a fake adapter script; behaviour is steered by env vars set per test.
func fake(t *testing.T) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	path = filepath.Join(dir, "adapter")
	script := `#!/bin/sh
echo "$* token=$BBAPPROVE_TOKEN" >> "$FAKE_LOG"
case "$1 $2" in
  "auth token") echo tok-from-adapter ;;
  "pr list") if [ -n "$FAKE_LIST_FAIL" ]; then echo "boom: list failed" >&2; exit 1; fi; printf '%s' "$FAKE_LIST" ;;
  "pr view") printf '{"url":"%s","number":9,"project":"P","repo":"v","title":"chore(deps): update v","author":{"email":"` + bot + `"},"headBranch":"renovate/v"}' "$3" ;;
  "pr review") case "$3" in *pull-requests/3) echo "approve denied" >&2; exit 1;; esac; sleep 0.05 ;;
  "pr merge") case "$3" in *pull-requests/4) echo "merge conflict" >&2; exit 1;; esac; sleep 0.05 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, log
}

func run(t *testing.T, env []string, args ...string) (code int, out, errOut string) {
	t.Helper()
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "disallow.txt"), []byte("# c\n\nanother-library\n"), 0o644)
	var so, se bytes.Buffer
	code = Run(context.Background(), Options{Args: args, Environ: env, RepoDir: repo, Stdout: &so, Stderr: &se})
	return code, so.String(), se.String()
}

func baseEnv(t *testing.T) ([]string, string) {
	ad, log := fake(t)
	return []string{"BBAPPROVE_ADAPTER=" + ad, "FAKE_LOG=" + log, "FAKE_LIST=" + listJSON, "NO_COLOR=1", "BBAPPROVE_JOBS=4"}, log
}

func readLog(t *testing.T, p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestDryRun(t *testing.T) {
	env, log := baseEnv(t)
	code, out, errOut := run(t, env)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 6 || !strings.HasPrefix(lines[0], "STATUS") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	wants := []string{"SKIP", "SKIP", "ELIGIBLE", "ELIGIBLE"}
	refs := []string{"P/a#1", "P/b#2", "P/c#3", "P/d#4"}
	for i, w := range wants {
		if !strings.HasPrefix(lines[i+1], w) || !strings.Contains(lines[i+1], refs[i]) {
			t.Errorf("line %d = %q, want %s %s", i+1, lines[i+1], w, refs[i])
		}
	}
	if !strings.Contains(lines[1], "— already approved by me") || !strings.Contains(lines[2], "disallowed dependency 'another-library'") {
		t.Errorf("reasons wrong:\n%s", out)
	}
	if lines[5] != "Dry run — pass --apply to approve and merge." {
		t.Errorf("last line %q", lines[5])
	}
	l := readLog(t, log)
	if strings.Contains(l, "pr review") || strings.Contains(l, "pr merge") {
		t.Errorf("dry run mutated: %s", l)
	}
	if !strings.Contains(l, "auth token") || !strings.Contains(l, "pr list --reviewer @me --state open token=tok-from-adapter") {
		t.Errorf("token not fetched/passed once: %s", l)
	}
	if strings.Count(l, "auth token") != 1 {
		t.Errorf("auth token called %d times", strings.Count(l, "auth token"))
	}
}

func TestEnvTokenSkipsAuth(t *testing.T) {
	env, log := baseEnv(t)
	if code, _, _ := run(t, append(env, "BBAPPROVE_TOKEN=given")); code != 0 {
		t.Fatal(code)
	}
	if l := readLog(t, log); strings.Contains(l, "auth token") || !strings.Contains(l, "token=given") {
		t.Errorf("log: %s", l)
	}
}

func TestApply(t *testing.T) {
	env, log := baseEnv(t)
	// make #1 and #2 eligible too by using different list: PRs 5..8 plus the failing 3 and 4
	list := `[
 {"url":"http://h/pull-requests/5","number":5,"project":"P","repo":"e","title":"chore(deps): update e","author":{"email":"` + bot + `"},"headBranch":"r"},
 {"url":"http://h/pull-requests/3","number":3,"project":"P","repo":"c","title":"chore(deps): update c","author":{"email":"` + bot + `"},"headBranch":"r"},
 {"url":"http://h/pull-requests/4","number":4,"project":"P","repo":"d","title":"chore(deps): update d","author":{"email":"` + bot + `"},"headBranch":"r"},
 {"url":"http://h/pull-requests/6","number":6,"project":"P","repo":"f","title":"chore(deps): update f to v3","author":{"email":"` + bot + `"},"headBranch":"r"}
]`
	env = append(env, "FAKE_LIST="+list)
	code, out, errOut := run(t, env, "--apply")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if strings.Contains(out, "Dry run") {
		t.Errorf("dry-run footer in apply mode: %s", out)
	}
	find := func(sub string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, sub) {
				return l
			}
		}
		t.Errorf("no line for %s in:\n%s", sub, out)
		return ""
	}
	if l := find("P/e#5"); !strings.HasPrefix(l, "MERGED") {
		t.Errorf("e: %q", l)
	}
	if l := find("P/c#3"); !strings.HasPrefix(l, "FAIL") || !strings.HasSuffix(l, "— approve denied") {
		t.Errorf("c: %q", l)
	}
	if l := find("P/d#4"); !strings.HasPrefix(l, "APPROVED, NOT MERGED") || !strings.HasSuffix(l, "— merge conflict") {
		t.Errorf("d: %q", l)
	}
	if l := find("P/f#6"); !strings.HasPrefix(l, "SKIP") || !strings.Contains(l, "major update") {
		t.Errorf("f: %q", l)
	}
	l := readLog(t, log)
	if strings.Contains(l, "pr merge http://h/pull-requests/3") {
		t.Errorf("merged after failed approve: %s", l)
	}
	if strings.Contains(l, "pull-requests/6") {
		t.Errorf("acted on skipped PR: %s", l)
	}
	for _, w := range []string{"pr review http://h/pull-requests/5 --approve", "pr merge http://h/pull-requests/5", "pr merge http://h/pull-requests/4"} {
		if !strings.Contains(l, w) {
			t.Errorf("missing call %q in %s", w, l)
		}
	}
}

func TestApplyParallelNoInterleave(t *testing.T) {
	env, _ := baseEnv(t)
	var items []string
	for i := 100; i < 130; i++ {
		n := string(rune('0'+i/100)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10))
		items = append(items, `{"url":"http://h/pull-requests/`+n+`","number":`+n+`,"project":"P","repo":"r","title":"chore(deps): update x","author":{"email":"`+bot+`"},"headBranch":"r"}`)
	}
	env = append(env, "FAKE_LIST=["+strings.Join(items, ",")+"]")
	code, out, _ := run(t, env, "--apply")
	if code != 0 {
		t.Fatal(code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 31 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "MERGED   ") || strings.Count(l, "MERGED") != 1 || !strings.HasSuffix(l, "chore(deps): update x") {
			t.Fatalf("interleaved/garbled line %q", l)
		}
	}
}

func TestPositionalURLs(t *testing.T) {
	env, log := baseEnv(t)
	code, out, _ := run(t, env, "http://h/pr/1", "http://h/pr/2")
	if code != 0 || strings.Count(out, "ELIGIBLE") != 2 {
		t.Fatalf("code %d out %s", code, out)
	}
	if l := readLog(t, log); !strings.Contains(l, "pr view http://h/pr/1") || !strings.Contains(l, "pr view http://h/pr/2") || strings.Contains(l, "pr list") {
		t.Errorf("log %s", l)
	}
}

func TestListFailure(t *testing.T) {
	env, _ := baseEnv(t)
	code, out, errOut := run(t, append(env, "FAKE_LIST_FAIL=1"))
	if code != 1 || !strings.Contains(errOut, "boom: list failed") || strings.Contains(out, "Dry run") {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}
}

func TestBadFlags(t *testing.T) {
	env, _ := baseEnv(t)
	for _, args := range [][]string{{"--nope"}, {"--disallow"}, {"-i"}} {
		if code, _, errOut := run(t, env, args...); code != 2 || errOut == "" {
			t.Errorf("%v: code %d err %q", args, code, errOut)
		}
	}
	if code, out, _ := run(t, env, "--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: %d %q", code, out)
	}
}

func TestDisallowFlag(t *testing.T) {
	env, _ := baseEnv(t)
	_, out, _ := run(t, env, "--disallow", "UPDA, te")
	if !strings.Contains(out, "disallowed dependency 'upda'") {
		t.Errorf("flag not applied:\n%s", out)
	}
}
