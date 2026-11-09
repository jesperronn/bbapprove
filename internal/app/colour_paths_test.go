package app

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"bbapprove/internal/testfix"
)

// colourCases are the outputs that were coloured before the shared style package reached the rest of the
// CLI: help, topics, doctor, --print-config and a dry run. They have their own goldens or tests; here they
// join pathCases so one table proves that EVERY command's output is coloured.
var colourCases = func() []pathCase {
	plainOpts := func(fx *fixture, args ...string) Options {
		return Options{Args: args, Environ: fx.env(), RepoDir: fx.repo}
	}
	cases := []pathCase{
		{"help", []string{"-h", "--help"}, func(t *testing.T, fx *fixture) Options { return plainOpts(fx, "--help") }},
		{"help-topic-list", []string{"help"}, func(t *testing.T, fx *fixture) Options { return plainOpts(fx, "help") }},
		{"doctor-help", []string{"doctor"}, func(t *testing.T, fx *fixture) Options { return plainOpts(fx, "doctor", "--help") }},
		{"doctor-report", []string{"doctor", "--offline"}, func(t *testing.T, fx *fixture) Options {
			o := plainOpts(fx, "doctor", "--offline")
			o.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
			o.Executable = func() (string, error) { return filepath.Join(fx.repo, "bin", "bbapprove"), nil }
			return o
		}},
		{"print-config", []string{"--print-config"}, func(t *testing.T, fx *fixture) Options {
			return plainOpts(fx, "--print-config", "--disallow", "a,b")
		}},
		{"dry-run-and-apply", []string{"--apply", "--hide-ignored"}, func(t *testing.T, fx *fixture) Options {
			return Options{Args: []string{"--hide-ignored"}, RepoDir: fx.repo, IgnorePath: filepath.Join(fx.base, "none"),
				Environ: fx.env("BBAPPROVE_TOKEN=t", "BBAPPROVE_ADAPTER="+testfix.Adapter(), "FAKE_BODY=echo '[]'", "BBAPPROVE_REPOSITORY=https://bb.example.com", "BBAPPROVE_AUTHOR_EMAIL=bot@example.com")}
		}},
	}
	for _, h := range helpTopics {
		topic := h.name
		cases = append(cases, pathCase{"help-topic-" + topic, []string{"help:" + topic}, func(t *testing.T, fx *fixture) Options {
			return plainOpts(fx, "help", topic)
		}})
	}
	return cases
}()

func allCases() []pathCase { return append(append([]pathCase{}, pathCases...), colourCases...) }

func hasSGR(s string) bool { return sgrRe.MatchString(s) }

// reportsColourEnv names cases whose text legitimately depends on the colour settings: the doctor report
// uses symbols instead of ok/FAIL/warn when coloured and prints the NO_COLOR / FORCE_COLOR it saw. Its plain
// and coloured forms are pinned by the goldens in internal/doctor; here it only has to be coloured.
var reportsColourEnv = map[string]bool{"doctor-report": true}

// TestEveryPathIsColoured runs every command, topic and error path with FORCE_COLOR=1 and checks that
// (a) some ANSI escape appears and (b) removing the escapes gives exactly the plain output. It also checks
// that NO_COLOR wins over FORCE_COLOR and that a run without either (a pipe) stays plain.
func TestEveryPathIsColoured(t *testing.T) {
	for _, c := range allCases() {
		t.Run(c.name, func(t *testing.T) {
			plain := runPathCase(t, c, "NO_COLOR=1")
			if hasSGR(plain.out) || hasSGR(plain.errOut) {
				t.Fatalf("NO_COLOR=1 output contains ANSI:\n%s", plain)
			}
			col := runPathCase(t, c, "FORCE_COLOR=1")
			if !hasSGR(col.out) && !hasSGR(col.errOut) {
				t.Errorf("FORCE_COLOR=1 output has no ANSI escape (uncoloured path):\n%s", col)
			}
			if col.code != plain.code {
				t.Errorf("exit code %d with colour, %d without", col.code, plain.code)
			}
			if got := stripSGR(col.out); got != plain.out && !reportsColourEnv[c.name] {
				t.Errorf("stdout differs after stripping colour\n--- stripped ---\n%s\n--- plain ---\n%s", got, plain.out)
			}
			if got := stripSGR(col.errOut); got != plain.errOut && !reportsColourEnv[c.name] {
				t.Errorf("stderr differs after stripping colour\n--- stripped ---\n%s\n--- plain ---\n%s", got, plain.errOut)
			}
			both := runPathCase(t, c, "FORCE_COLOR=1", "NO_COLOR=1")
			if hasSGR(both.out) || hasSGR(both.errOut) || (both.String() != plain.String() && !reportsColourEnv[c.name]) {
				t.Errorf("NO_COLOR=1 must win over FORCE_COLOR=1:\n%s", both)
			}
			wasTTY := false
			pipe := runPathCaseWith(t, c, func(o *Options) { wasTTY = o.IsTTY || o.StderrTTY })
			if !wasTTY && (hasSGR(pipe.out) || hasSGR(pipe.errOut) || (!reportsColourEnv[c.name] && pipe.String() != plain.String())) { // cases that simulate a terminal (prompts) are coloured by design
				t.Errorf("without a terminal and without FORCE_COLOR the output must be plain:\n%s", pipe)
			}
		})
	}
}

// TestStreamsAreColouredIndependently: a terminal on one stream colours only that stream.
func TestStreamsAreColouredIndependently(t *testing.T) {
	var both *pathCase // writes to stdout (manual steps) and to stderr (the error)
	for i := range pathCases {
		if pathCases[i].name == "update-archive-gh-missing" {
			both = &pathCases[i]
		}
	}
	if both == nil {
		t.Fatal("case missing")
	}
	outTTY := runPathCaseWith(t, *both, func(o *Options) { o.IsTTY, o.StderrTTY = true, false })
	if !hasSGR(outTTY.out) || hasSGR(outTTY.errOut) {
		t.Errorf("stdout is a terminal, stderr is not: stdout coloured=%v stderr coloured=%v\n%s", hasSGR(outTTY.out), hasSGR(outTTY.errOut), outTTY)
	}
	errTTY := runPathCaseWith(t, *both, func(o *Options) { o.IsTTY, o.StderrTTY = false, true })
	if hasSGR(errTTY.out) || !hasSGR(errTTY.errOut) {
		t.Errorf("stderr is a terminal, stdout is not: stdout coloured=%v stderr coloured=%v\n%s", hasSGR(errTTY.out), hasSGR(errTTY.errOut), errTTY)
	}
	bothTTY := runPathCaseWith(t, *both, func(o *Options) { o.IsTTY, o.StderrTTY = true, true })
	if !hasSGR(bothTTY.out) || !hasSGR(bothTTY.errOut) {
		t.Errorf("both are terminals but output is plain:\n%s", bothTTY)
	}
}

// colourExempt lists help entries that need no coloured-output case, each with its reason.
var colourExempt = map[string]string{
	// scripts parse the version, so it stays plain on purpose (see cmd/bbapprove/main.go).
	"-V":        "--version is plain on purpose: scripts parse it",
	"--version": "--version is plain on purpose: scripts parse it",
	// the TUI paints its own full-screen view (internal/tui); the flag itself prints nothing.
	"--no-mouse": "only changes mouse capture inside the full-screen TUI",
}

// helpEntries reads the flags and subcommands the top-level help advertises, plus the help topics.
func helpEntries(t *testing.T) []string {
	t.Helper()
	text := usage("v1")
	set := map[string]bool{"help": true}
	flagRe := regexp.MustCompile(`(?:^|[\s\[(,])(-{1,2}[A-Za-z][A-Za-z-]*)`)
	for _, m := range flagRe.FindAllStringSubmatch(text, -1) {
		set[m[1]] = true
	}
	if !strings.Contains(text, "bbapprove doctor") {
		t.Fatal("help no longer lists the doctor subcommand; update this guard")
	}
	set["doctor"] = true
	for _, h := range helpTopics {
		set["help:"+h.name] = true
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryCommandHasAColourCase fails when a flag, subcommand or help topic shown by the help has no
// case in the colour table, so a new command cannot ship uncoloured. Add the command to pathCases (give
// the case a covers entry naming it) or, with a reason, to colourExempt.
func TestEveryCommandHasAColourCase(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range allCases() {
		for _, k := range c.covers {
			covered[k] = true
		}
	}
	entries := helpEntries(t)
	for _, want := range []string{"--outdated", "--update", "--init", "-i", "--version", "doctor", "help:rules"} {
		found := false
		for _, e := range entries {
			found = found || e == want
		}
		if !found {
			t.Fatalf("the help scan no longer finds %q; the guard is broken (entries: %v)", want, entries)
		}
	}
	known := map[string]bool{}
	for _, e := range entries {
		known[e] = true
		if reason, ok := colourExempt[e]; ok {
			if reason == "" {
				t.Errorf("exemption for %s needs a reason", e)
			}
			continue
		}
		if !covered[e] {
			t.Errorf("%s is in the help but no coloured-output case covers it (see pathCases / colourCases)", e)
		}
	}
	for k := range covered {
		if !known[k] && !strings.HasPrefix(k, "error:") {
			t.Errorf("a case covers %q, which the help does not list", k)
		}
	}
	for k := range colourExempt {
		if !known[k] {
			t.Errorf("exemption %q is not in the help any more", k)
		}
	}
}
