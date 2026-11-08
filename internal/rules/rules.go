// Package rules decides whether a PR may be auto-approved.
package rules

import (
	"os"
	"regexp"
	"strings"

	"bbapprove/internal/adapter"
	"bbapprove/internal/ignore"
)

var (
	titleRe = regexp.MustCompile(`^(chore|fix)\(deps\): `)
	majorRe = regexp.MustCompile(` to v?[0-9]+ *(\(.*\))?$`)
)

// Config holds eligibility settings.
type Config struct {
	AuthorEmail string
	Disallow    []string // normalized by Normalize
}

// Normalize lowercases and strips all whitespace from an item; empty results are dropped by callers.
func Normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

// LoadDisallow returns file entries ('#' lines and blanks ignored; a missing file is fine)
// followed by comma-separated extra items, all normalized, empties dropped.
func LoadDisallow(file, extra string) []string {
	var items []string
	add := func(s string) {
		if n := Normalize(s); n != "" {
			items = append(items, n)
		}
	}
	if b, err := os.ReadFile(file); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			add(line)
		}
	}
	for _, s := range strings.Split(extra, ",") {
		add(s)
	}
	return items
}

// Check returns ("", true) when eligible, otherwise the skip reason.
func (c Config) Check(pr adapter.PR) (string, bool) {
	if pr.Author.Email != c.AuthorEmail {
		return "author '" + pr.Author.Email + "' is not the renovate bot", false
	}
	if !titleRe.MatchString(pr.Title) {
		return "subject does not start with chore(deps): or fix(deps):", false
	}
	lower := strings.ToLower(pr.Title)
	for _, it := range c.Disallow {
		if n := Normalize(it); n != "" && strings.Contains(lower, n) {
			return "disallowed dependency '" + n + "'", false
		}
	}
	if strings.HasPrefix(pr.HeadBranch, "renovate/major-") || majorRe.MatchString(pr.Title) {
		return "major update", false
	}
	if pr.ApprovedByMe {
		return "already approved by me", false
	}
	return "", true
}

// Kind is the classification of a PR for display and for acting on it.
type Kind int

const (
	// Eligible means the PR may be approved and merged.
	Eligible Kind = iota
	// Skip means a rule (author, subject, disallow, major, already approved) rejects the PR.
	Skip
	// Ignored means the PR matches the per-user ignore list; this wins over every Skip reason.
	Ignored
)

// IgnoreReasonPrefix starts the reason of an Ignored classification; the matching entry follows.
const IgnoreReasonPrefix = "on your ignore list: "

// Classify is the single classification shared by the CLI and the TUI so they can never disagree.
// check is the eligibility rule (Config.Check); ignored is the per-user ignore list. The ignore list
// is consulted FIRST: a PR matching an ignore entry is Ignored whatever else would skip it
// (author, subject, disallow, major, already approved), and is never eligible nor acted on.
// Removing the entry restores the PR's own classification, including its original Skip reason.
func Classify(check func(adapter.PR) (string, bool), ignored []string, pr adapter.PR) (Kind, string) {
	if entry, ok := ignore.Matches(ignored, pr.Title); ok {
		return Ignored, IgnoreReasonPrefix + entry
	}
	if reason, ok := check(pr); !ok {
		return Skip, reason
	}
	return Eligible, ""
}

// Classify applies Config.Check and then the ignore list; see the package-level Classify.
func (c Config) Classify(ignored []string, pr adapter.PR) (Kind, string) {
	return Classify(c.Check, ignored, pr)
}
