package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bbapprove/internal/adapter"
)

const bot = "renovate-bot@example.com"

func pr(email, title, branch string, approved bool) adapter.PR {
	p := adapter.PR{Title: title, HeadBranch: branch, ApprovedByMe: approved}
	p.Author.Email = email
	return p
}

func TestCheck(t *testing.T) {
	cfg := Config{AuthorEmail: bot, Disallow: []string{"some-library", "another-library", "a b"}}
	tests := []struct {
		name   string
		pr     adapter.PR
		reason string
	}{
		{"eligible", pr(bot, "chore(deps): update foo to v1.2.3", "renovate/foo", false), ""},
		{"fix prefix", pr(bot, "fix(deps): update foo", "renovate/foo", false), ""},
		{"author", pr("x@y", "chore(deps): update foo", "", false), "author 'x@y' is not the renovate bot"},
		{"empty author", pr("", "chore(deps): update foo", "", false), "author '' is not the renovate bot"},
		{"subject", pr(bot, "feat: thing", "", false), "subject does not start with chore(deps): or fix(deps):"},
		{"subject needs space", pr(bot, "chore(deps):x", "", false), "subject does not start with chore(deps): or fix(deps):"},
		{"disallow case-insens", pr(bot, "chore(deps): update SOME-LIBRARY to 1.2", "", false), "disallowed dependency 'some-library'"},
		{"disallow substring", pr(bot, "chore(deps): update @org/another-library-js", "", false), "disallowed dependency 'another-library'"},
		{"disallow whitespace stripped", pr(bot, "chore(deps): update ab", "", false), "disallowed dependency 'ab'"},
		{"major branch", pr(bot, "chore(deps): update foo", "renovate/major-foo", false), "major update"},
		{"major title", pr(bot, "chore(deps): update foo to v5", "renovate/foo", false), "major update"},
		{"major title no v", pr(bot, "chore(deps): update foo to 5", "renovate/foo", false), "major update"},
		{"major title suffix", pr(bot, "chore(deps): update foo to v5 (main)", "renovate/foo", false), "major update"},
		{"minor not major", pr(bot, "chore(deps): update foo to v5.1", "renovate/foo", false), ""},
		{"approved", pr(bot, "chore(deps): update foo", "renovate/foo", true), "already approved by me"},
		// ordering: author beats subject beats disallow beats major beats approved
		{"order author>subject", pr("x", "feat: another-library", "renovate/major-x", true), "author 'x' is not the renovate bot"},
		{"order subject>disallow", pr(bot, "feat: another-library", "renovate/major-x", true), "subject does not start with chore(deps): or fix(deps):"},
		{"order disallow>major", pr(bot, "chore(deps): another-library to v2", "renovate/major-x", true), "disallowed dependency 'another-library'"},
		{"order major>approved", pr(bot, "chore(deps): foo to v2", "", true), "major update"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := cfg.Check(tc.pr)
			if reason != tc.reason || ok != (tc.reason == "") {
				t.Fatalf("got (%q,%v), want reason %q", reason, ok, tc.reason)
			}
		})
	}
}

func TestLoadDisallow(t *testing.T) {
	f := filepath.Join(t.TempDir(), "d.txt")
	body := "# comment\n\n  Some-Library  \n   # indented comment\nkey cloak\n\t\nfoo\n"
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadDisallow(f, " Bar , ,baz")
	want := []string{"some-library", "another-library", "foo", "bar", "baz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := LoadDisallow(filepath.Join(t.TempDir(), "missing"), ""); len(got) != 0 {
		t.Fatalf("missing file: got %v", got)
	}
}

func TestClassify(t *testing.T) {
	cfg := Config{AuthorEmail: bot, Disallow: []string{"another-library", "some-library"}}
	ign := []string{"npm", "another-library"}
	tests := []struct {
		name   string
		pr     adapter.PR
		kind   Kind
		reason string
	}{
		{"eligible", pr(bot, "chore(deps): update lodash to v4.1", "r", false), Eligible, ""},
		{"ignored", pr(bot, "chore(deps): update dependency NPM to v1.2", "r", false), Ignored, "on your ignore list: npm"},
		{"ignore beats disallow", pr(bot, "chore(deps): update another-library to v1.2", "r", false), Ignored, "on your ignore list: another-library"},
		{"disallow still skips when not ignored", pr(bot, "chore(deps): update some-library to v1.2", "r", false), Skip, "disallowed dependency 'some-library'"},
		{"ignore beats major", pr(bot, "chore(deps): update npm to v9", "r", false), Ignored, "on your ignore list: npm"},
		{"ignore beats approved", pr(bot, "chore(deps): update npm to v1.1", "r", true), Ignored, "on your ignore list: npm"},
		{"ignore beats author", pr("x@y", "chore(deps): update npm", "r", false), Ignored, "on your ignore list: npm"},
		{"ignore beats subject", pr(bot, "feat: npm", "r", false), Ignored, "on your ignore list: npm"},
		{"subject skips when not ignored", pr(bot, "feat: lodash", "r", false), Skip, "subject does not start with chore(deps): or fix(deps):"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, why := cfg.Classify(ign, tc.pr)
			if k != tc.kind || why != tc.reason {
				t.Fatalf("got (%v, %q), want (%v, %q)", k, why, tc.kind, tc.reason)
			}
		})
	}
	if k, _ := cfg.Classify(nil, pr(bot, "chore(deps): update npm to v1.2", "r", false)); k != Eligible {
		t.Fatal("empty ignore list must not ignore")
	}
	// Un-ignoring restores the original skip reason.
	if k, why := cfg.Classify(nil, pr(bot, "chore(deps): update npm to v9", "r", false)); k != Skip || why != "major update" {
		t.Fatalf("un-ignored major: (%v, %q)", k, why)
	}
}
