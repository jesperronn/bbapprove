package app

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"bbapprove/internal/style"
)

// Drift check between README.md and the help texts (`bbapprove --help` and every `bbapprove help TOPIC`).
//
// The help is the reference for flags and environment variables; the README is a short quick-start that
// links to it instead of repeating it. The rule is therefore:
//
//  1. Every --flag and BBAPPROVE_* name the README mentions must exist in some help text (no stale docs).
//  2. Every help text must be reachable from the README: it must contain the exact command that shows it
//     (`bbapprove --help` for the top level, `bbapprove help NAME` for a topic, one link per topic). So a flag or variable that only the help lists is covered by a topic the README links to, and
//     a new topic cannot be forgotten.
//
// Flags and variables are collected from the plain output, so the check follows what a user sees.

var (
	longFlagRe = regexp.MustCompile(`(?:^|[^\w-])(--[a-z][a-z-]*)`)
	envVarRe   = regexp.MustCompile(`\bBBAPPROVE_[A-Z_]+\b`)
)

// linkRe matches one topic's own link: `bbapprove help NAME` as a word, so "help config" is not satisfied by
// "help configuration" and a topic cannot hide in a "rules | tui | ..." list.
func linkRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`bbapprove help ` + regexp.QuoteMeta(name) + `\b`)
}

func tokens(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range longFlagRe.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	for _, m := range envVarRe.FindAllString(text, -1) {
		out[m] = true
	}
	return out
}

func sorted(m map[string]bool) []string {
	var s []string
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

func TestReadmeMatchesHelp(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	plain := style.Theme{}

	helpTokens := tokens(usage("v1"))
	for _, h := range helpTopics {
		text, _ := topicUsageFor(plain, h.name)
		for k := range tokens(text) {
			helpTokens[k] = true
		}
	}

	// 1. README -> help. Lines about other programs (shasum, the bin/ scripts) have their own flags.
	var own []string
	for _, l := range strings.Split(readme, "\n") {
		if !strings.Contains(l, "shasum ") && !strings.Contains(l, "bin/build") {
			own = append(own, l)
		}
	}
	for _, k := range sorted(tokens(strings.Join(own, "\n"))) {
		if !helpTokens[k] {
			t.Errorf("README.md mentions %s, which no help text mentions: remove it or document it in internal/app/help.go", k)
		}
	}

	// 2. help -> README: every help text is linked.
	if !strings.Contains(readme, "bbapprove --help") {
		t.Error("README.md must point at `bbapprove --help`, the flag reference")
	}
	for _, h := range helpTopics {
		if !linkRe(h.name).MatchString(readme) {
			t.Errorf("README.md does not link help topic %q: mention `bbapprove help %s`, or flags and variables documented only there would be unreachable", h.name, h.name)
		}
	}
}
