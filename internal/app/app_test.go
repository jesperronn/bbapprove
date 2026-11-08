package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bot = "renovate-bot@example.com"

const listJSON = `[
 {"url":"http://h/projects/P/repos/a/pull-requests/1","number":1,"project":"P","repo":"a","title":"chore(deps): update a","author":{"email":"` + bot + `"},"headBranch":"renovate/a","build":"passed","qualityGate":"passed","approvals":1,"approvedByMe":true},
 {"url":"http://h/projects/P/repos/b/pull-requests/2","number":2,"project":"P","repo":"b","title":"chore(deps): update some-library","author":{"email":"` + bot + `"},"headBranch":"renovate/k"},
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
echo "base=$BBAPPROVE_BASE_URL name=$BBAPPROVE_TOKEN_NAME" >> "$FAKE_LOG.env"
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
	os.WriteFile(filepath.Join(repo, "disallow.txt"), []byte("# c\n\nsome-library\n"), 0o644)
	var so, se bytes.Buffer
	code = Run(context.Background(), Options{Args: args, Environ: env, RepoDir: repo, Stdout: &so, Stderr: &se})
	return code, so.String(), se.String()
}

func baseEnv(t *testing.T) ([]string, string) {
	ad, log := fake(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // never read the real per-user ignore list
	return []string{"BBAPPROVE_ADAPTER=" + ad, "FAKE_LOG=" + log, "FAKE_LIST=" + listJSON, "NO_COLOR=1", "BBAPPROVE_JOBS=4",
		"BBAPPROVE_REPOSITORY=https://bitbucket.example.com", "BBAPPROVE_AUTHOR_EMAIL=" + bot}, log
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
	wants := []string{"SKIP", "SKIP", "", ""}
	refs := []string{"P/a#1", "P/b#2", "P/c#3", "P/d#4"}
	for i, w := range wants {
		if !strings.HasPrefix(lines[i+1], w+strings.Repeat(" ", 13-len(w))) || w == "" && strings.TrimSpace(lines[i+1][:13]) != "" || !strings.Contains(lines[i+1], refs[i]) {
			t.Errorf("line %d = %q, want %s %s", i+1, lines[i+1], w, refs[i])
		}
	}
	if !strings.Contains(lines[1], "— already approved by me") || !strings.Contains(lines[2], "disallowed dependency 'some-library'") {
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
	if l := find("P/d#4"); !strings.HasPrefix(l, "NOT MERGED") || !strings.HasSuffix(l, "— merge conflict") {
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
		if !strings.HasPrefix(l, "MERGED     ") || strings.Count(l, "MERGED") != 1 || !strings.HasSuffix(l, "chore(deps): update x") {
			t.Fatalf("interleaved/garbled line %q", l)
		}
	}
}

func TestPositionalURLs(t *testing.T) {
	env, log := baseEnv(t)
	code, out, _ := run(t, env, "http://h/pr/1", "http://h/pr/2")
	if code != 0 || strings.Count(out, "P/v#9") != 2 || strings.Contains(out, "ELIGIBLE") {
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

func TestUsageFirstLineCarriesVersion(t *testing.T) {
	if first := strings.SplitN(usage("v1.2.3"), "\n", 2)[0]; !strings.HasPrefix(first, "bbapprove v1.2.3 — ") {
		t.Errorf("first line: %q", first)
	}
	if !strings.HasPrefix(usage(""), "bbapprove dev — ") {
		t.Errorf("empty version should read dev")
	}
}

func writeIgnore(t *testing.T, content string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bbapprove")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func prJSON(n int, title string) string {
	return fmt.Sprintf(`{"url":"http://h/pull-requests/%d","number":%d,"project":"P","repo":"r%d","title":%q,"author":{"email":"%s"},"headBranch":"r"}`, n, n, n, title, bot)
}

func ignoreList() string {
	return "[" + strings.Join([]string{
		prJSON(11, "chore(deps): update dependency npm to v1.2.3"),
		prJSON(12, "chore(deps): update dependency lodash to v4.17.21"),
		prJSON(13, "chore(deps): update some-library to v1.2.3"),  // disallowed AND ignored
		prJSON(14, "chore(deps): update dependency npm to v9"),    // major AND ignored
		prJSON(16, "chore(deps): update dependency react to v19"), // major, not ignored
	}, ",") + "]"
}

func lineFor(out, sub string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestIgnoreListHonouredInDryRun(t *testing.T) {
	env, log := baseEnv(t)
	writeIgnore(t, "# mine\nnpm\nsome-library\n")
	env = append(env, "FAKE_LIST="+ignoreList())
	code, out, errOut := run(t, env)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	tests := []struct{ ref, prefix, suffix string }{
		{"P/r11#11", "IGNORED     ", "— on your ignore list: npm"},
		{"P/r12#12", strings.Repeat(" ", 13), "update dependency lodash to v4.17.21"}, // eligible: blank status
		{"P/r13#13", "IGNORED     ", "— on your ignore list: some-library"},           // ignore beats disallow
		{"P/r14#14", "IGNORED     ", "— on your ignore list: npm"},                    // ignore beats major
		{"P/r16#16", "SKIP        ", "— major update"},                                // not ignored: reason unchanged
	}
	for _, tc := range tests {
		l := lineFor(out, tc.ref)
		if !strings.HasPrefix(l, tc.prefix) || !strings.HasSuffix(l, tc.suffix) {
			t.Errorf("%s: %q want prefix %q suffix %q", tc.ref, l, tc.prefix, tc.suffix)
		}
	}
	if strings.Contains(out, "ignored (hidden)") || !strings.Contains(out, "Dry run") {
		t.Errorf("footer:\n%s", out)
	}
	if l := readLog(t, log); strings.Contains(l, "pr review") || strings.Contains(l, "pr merge") {
		t.Errorf("dry run mutated: %s", l)
	}
}

func TestApplyNeverTouchesIgnoredPR(t *testing.T) {
	env, log := baseEnv(t)
	writeIgnore(t, "npm\n")
	env = append(env, "FAKE_LIST="+ignoreList())
	code, out, errOut := run(t, env, "--apply")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if l := lineFor(out, "P/r11#11"); !strings.HasPrefix(l, "IGNORED") || !strings.HasSuffix(l, "— on your ignore list: npm") {
		t.Errorf("ignored row: %q", l)
	}
	if l := lineFor(out, "P/r12#12"); !strings.HasPrefix(l, "MERGED") {
		t.Errorf("lodash must merge: %q", l)
	}
	l := readLog(t, log)
	if strings.Contains(l, "pull-requests/11") || strings.Contains(l, "pull-requests/14") {
		t.Fatalf("ignored PR was acted on:\n%s", l)
	}
	if !strings.Contains(l, "pr review http://h/pull-requests/12 --approve") || !strings.Contains(l, "pr merge http://h/pull-requests/12") {
		t.Errorf("eligible PR not merged:\n%s", l)
	}
}

func TestHideIgnored(t *testing.T) {
	for _, apply := range []bool{false, true} {
		env, log := baseEnv(t)
		writeIgnore(t, "npm\n")
		env = append(env, "FAKE_LIST="+prListOf(prJSON(11, "chore(deps): update dependency npm to v1.2.3"), prJSON(12, "chore(deps): update lodash to v4.1"), prJSON(15, "chore(deps): update npm-run-all to v1.1")))
		args := []string{"--hide-ignored"}
		if apply {
			args = append(args, "--apply")
		}
		code, out, _ := run(t, env, args...)
		if code != 0 || strings.Contains(out, "IGNORED") || strings.Contains(out, "P/r11#11") || strings.Contains(out, "P/r15#15") {
			t.Fatalf("apply=%v hidden rows shown:\n%s", apply, out)
		}
		if !strings.Contains(out, "2 ignored (hidden)") || !strings.Contains(out, "P/r12#12") {
			t.Errorf("apply=%v footer/eligible:\n%s", apply, out)
		}
		if strings.Contains(readLog(t, log), "pull-requests/11") {
			t.Errorf("apply=%v acted on hidden ignored PR", apply)
		}
	}
}

func prListOf(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func TestMissingIgnoreFileIsEmpty(t *testing.T) {
	env, _ := baseEnv(t)
	env = append(env, "FAKE_LIST="+ignoreList())
	_, out, _ := run(t, env)
	if strings.Contains(out, "IGNORED") {
		t.Errorf("no ignore file, yet IGNORED:\n%s", out)
	}
}

func TestUnreadableIgnoreListFailsClosed(t *testing.T) {
	env, log := baseEnv(t)
	// a directory where the file should be: exists but cannot be read
	if err := os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bbapprove", "ignore"), 0o755); err != nil {
		t.Fatal(err)
	}
	env = append(env, "FAKE_LIST="+ignoreList())
	code, out, errOut := run(t, env, "--apply")
	if code != 1 || !strings.Contains(errOut, "cannot read ignore list") || out != "" {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}
	if l := readLog(t, log); strings.Contains(l, "pr review") || strings.Contains(l, "pr merge") {
		t.Fatalf("acted despite unreadable ignore list: %s", l)
	}
}

func TestIgnoreListFromEnvironXDG(t *testing.T) {
	env, _ := baseEnv(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bbapprove"), 0o755)
	os.WriteFile(filepath.Join(dir, "bbapprove", "ignore"), []byte("lodash\n"), 0o644)
	env = append(env, "XDG_CONFIG_HOME="+dir, "FAKE_LIST="+ignoreList())
	_, out, _ := run(t, env)
	if l := lineFor(out, "P/r12#12"); !strings.HasPrefix(l, "IGNORED") {
		t.Errorf("environ XDG_CONFIG_HOME not honoured: %q", l)
	}
}

func TestHelpMentionsIgnoreAndStatus(t *testing.T) {
	for _, w := range []string{"--hide-ignored", "ignore list", "IGNORED", "h or .", "COMMENTING…", "N ignored (hidden)"} {
		if !strings.Contains(usage("x"), w) {
			t.Errorf("help lacks %q", w)
		}
	}
}

// runIn is run with an explicit repo dir, so a test can put a config file there.
func runIn(t *testing.T, repo string, env []string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	code = Run(context.Background(), Options{Args: args, Environ: env, RepoDir: repo, Stdout: &so, Stderr: &se})
	return code, so.String(), se.String()
}

// bareEnv has the fake adapter but no repository / author email, so settings must come from elsewhere.
func bareEnv(t *testing.T) ([]string, string) {
	env, log := baseEnv(t)
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "BBAPPROVE_REPOSITORY=") && !strings.HasPrefix(kv, "BBAPPROVE_AUTHOR_EMAIL=") {
			out = append(out, kv)
		}
	}
	return out, log
}

func TestFirstRunWithoutConfigFailsHelpfully(t *testing.T) {
	env, log := bareEnv(t)
	repo := t.TempDir()
	code, out, errOut := runIn(t, repo, env)
	if code != 1 || out != "" {
		t.Fatalf("code %d out %q", code, out)
	}
	for _, w := range []string{`"repository" is not set`, "cp bbapprove.config.template bbapprove.config.toml",
		"Searched: " + filepath.Join(repo, "bbapprove.config.toml")} {
		if !strings.Contains(errOut, w) {
			t.Errorf("stderr lacks %q:\n%s", w, errOut)
		}
	}
	if readLog(t, log) != "" {
		t.Error("the adapter must not run before the config is complete")
	}
	// repository set, author_email still missing
	code, _, errOut = runIn(t, repo, append(env, "BBAPPROVE_REPOSITORY=https://bitbucket.example.com"))
	if code != 1 || !strings.Contains(errOut, `"author_email" is not set`) {
		t.Fatalf("code %d err %q", code, errOut)
	}
}

func TestConfigFileDrivesRunAndAdapterEnv(t *testing.T) {
	env, log := bareEnv(t)
	repo := t.TempDir()
	cfg := "repository = \"https://file.example.com\"\nuser_token_name = \"FILE_ITEM\"\nauthor_email = \"" + bot + "\"\ndisallow = [\"some-library\"]\n"
	os.WriteFile(filepath.Join(repo, "bbapprove.config.toml"), []byte(cfg), 0o644)
	code, out, errOut := runIn(t, repo, env)
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if !strings.Contains(out, "disallowed dependency 'some-library'") {
		t.Errorf("disallow from the file not applied:\n%s", out)
	}
	envLog, _ := os.ReadFile(log + ".env")
	if !strings.Contains(string(envLog), "base=https://file.example.com name=FILE_ITEM") {
		t.Errorf("adapter child env:\n%s", envLog)
	}

	// flag beats env beats file for the item name, and reaches the adapter
	os.Remove(log + ".env")
	code, _, errOut = runIn(t, repo, append(env, "BBAPPROVE_TOKEN_NAME=ENV_ITEM", "BBAPPROVE_REPOSITORY=https://env.example.com"), "--token-name", "FLAG_ITEM")
	if code != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
	envLog, _ = os.ReadFile(log + ".env")
	if !strings.Contains(string(envLog), "base=https://env.example.com name=FLAG_ITEM") {
		t.Errorf("adapter child env:\n%s", envLog)
	}
}

func TestConfigFlagAndErrors(t *testing.T) {
	env, _ := bareEnv(t)
	repo := t.TempDir()
	good := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(good, []byte("repository = \"https://h.example.com\"\nauthor_email = \""+bot+"\"\nbogus = 1\n"), 0o644)
	code, _, errOut := runIn(t, repo, env, "--config", good)
	if code != 0 || !strings.Contains(errOut, `warning:`) || !strings.Contains(errOut, `unknown key "bogus"`) {
		t.Fatalf("unknown key: code %d err %q", code, errOut)
	}
	code, _, errOut = runIn(t, repo, env, "--config="+filepath.Join(repo, "missing.toml"))
	if code != 1 || !strings.Contains(errOut, "missing.toml") {
		t.Fatalf("missing: code %d err %q", code, errOut)
	}
	code, _, errOut = runIn(t, repo, append(env, "BBAPPROVE_CONFIG="+filepath.Join(repo, "missing2.toml")))
	if code != 1 || !strings.Contains(errOut, "missing2.toml") {
		t.Fatalf("env missing: code %d err %q", code, errOut)
	}
	bad := filepath.Join(repo, "bad.toml")
	os.WriteFile(bad, []byte("a = 1\nb = \n"), 0o644)
	code, _, errOut = runIn(t, repo, env, "--config", bad)
	if code != 1 || !strings.Contains(errOut, bad) || !strings.Contains(errOut, "line 2") {
		t.Fatalf("malformed: code %d err %q", code, errOut)
	}
	for _, flag := range []string{"--config", "--token-name"} {
		if code, _, errOut = runIn(t, repo, env, flag); code != 2 || !strings.Contains(errOut, flag+" needs a value") {
			t.Errorf("%s: code %d err %q", flag, code, errOut)
		}
	}
}

func TestPrintConfig(t *testing.T) {
	env, log := bareEnv(t)
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "bbapprove.config.toml"),
		[]byte("repository = \"https://file.example.com\"\nauthor_email = \"file-bot@example.com\"\ndisallow = [\"some-library\"]\n"), 0o644)
	code, out, errOut := runIn(t, repo, append(env, "BBAPPROVE_TOKEN=s3cret-value", "BBAPPROVE_AUTHOR_EMAIL=env-bot@example.com"),
		"--print-config", "--token-name", "FLAG_ITEM", "--token", "also-secret")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
	for _, w := range []string{
		"config file:     " + filepath.Join(repo, "bbapprove.config.toml"),
		"repository:      https://file.example.com  [file]",
		"user_token_name: FLAG_ITEM  [flag]",
		"author_email:    env-bot@example.com  [env]",
		"type:            bitbucket  [default]",
		"disallow:        some-library",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "secret") {
		t.Errorf("secret leaked:\n%s", out)
	}
	if readLog(t, log) != "" {
		t.Error("--print-config must not call the adapter")
	}
	// it works even when required settings are missing, and together with -i
	code, out, _ = runIn(t, t.TempDir(), env, "-i", "--print-config")
	if code != 0 || !strings.Contains(out, "repository:      (not set)  [unset]") {
		t.Errorf("code %d out %q", code, out)
	}
}
