package release

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/")

func TestParseCommit(t *testing.T) {
	tests := []struct {
		name, subject, body string
		want                Commit
	}{
		{"plain", "feat: add thing", "", Commit{Type: "feat", Description: "add thing"}},
		{"scoped", "fix(tui): stop flicker", "", Commit{Type: "fix", Scope: "tui", Description: "stop flicker"}},
		{"bang", "feat!: drop flag", "", Commit{Type: "feat", Breaking: true, Description: "drop flag"}},
		{"scoped bang", "refactor(config)!: rename key", "", Commit{Type: "refactor", Scope: "config", Breaking: true, Description: "rename key"}},
		{"footer space", "feat: x", "Prose.\n\nBREAKING CHANGE: old flag gone", Commit{Type: "feat", Breaking: true, Description: "x"}},
		{"footer dash", "fix: x", "BREAKING-CHANGE: yes", Commit{Type: "fix", Breaking: true, Description: "x"}},
		{"footer mid-line is not a footer", "fix: x", "see BREAKING CHANGE: in docs", Commit{Type: "fix", Description: "x"}},
		{"type is lower-cased", "Feat: Add", "", Commit{Type: "feat", Description: "Add"}},
		{"merge commit", "Merge pull request #3 from a/b", "", Commit{}},
		{"stray text", "wip", "", Commit{}},
		{"no space after colon", "feat:x", "", Commit{}},
		{"revert", "revert: undo thing", "", Commit{Type: "revert", Description: "undo thing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseCommit(tt.subject, tt.body); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func cs(subjects ...string) []Commit {
	var out []Commit
	for _, s := range subjects {
		out = append(out, ParseCommit(s, ""))
	}
	return out
}

func TestNextVersion(t *testing.T) {
	v := func(s string) Version {
		x, err := ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	tests := []struct {
		name    string
		last    string
		commits []Commit
		want    string
		bump    Bump
		ok      bool
	}{
		{"feat is minor", "v1.2.3", cs("feat: a"), "1.3.0", BumpMinor, true},
		{"fix is patch", "v1.2.3", cs("fix: a"), "1.2.4", BumpPatch, true},
		{"perf is patch", "v1.2.3", cs("perf: a"), "1.2.4", BumpPatch, true},
		{"bang is major", "v1.2.3", cs("feat!: a", "fix: b"), "2.0.0", BumpMajor, true},
		{"footer is major", "v1.2.3", []Commit{ParseCommit("fix: a", "BREAKING CHANGE: b")}, "2.0.0", BumpMajor, true},
		{"largest wins", "v1.2.3", cs("fix: a", "feat: b", "chore: c"), "1.3.0", BumpMinor, true},
		{"only chores", "v1.2.3", cs("chore: a", "docs: b", "test: c", "refactor: d", "ci: e", "build: f", "style: g"), "", BumpNone, false},
		{"non-conventional only", "v1.2.3", cs("Merge branch x", "wip"), "", BumpNone, false},
		{"empty", "v1.2.3", nil, "", BumpNone, false},
		{"revert alone is not releasable", "v1.2.3", cs("revert: a"), "", BumpNone, false},
		{"pre-release core wins over fix", "v1.0.0-rc.2", cs("fix: a"), "1.0.0", BumpPatch, true},
		{"pre-release core wins over feat", "v1.0.0-rc.2", cs("feat: a"), "1.0.0", BumpMinor, true},
		{"pre-release nothing releasable", "v1.0.0-rc.2", cs("docs: a"), "", BumpNone, false},
		{"pre-1.0 breaking is major", "v0.3.1", cs("feat!: a"), "1.0.0", BumpMajor, true},
		{"pre-1.0 feat is minor", "v0.3.1", cs("feat: a"), "0.4.0", BumpMinor, true},
		{"no tag yet", "v0.0.0", cs("feat: a"), "0.1.0", BumpMinor, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, bump, ok := NextVersion(v(tt.last), tt.commits)
			if ok != tt.ok || bump != tt.bump {
				t.Fatalf("ok=%v bump=%v, want ok=%v bump=%v", ok, bump, tt.ok, tt.bump)
			}
			if ok && next.String() != tt.want {
				t.Errorf("next = %s, want %s", next, tt.want)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	for _, s := range []string{"1.2.3", "v1.2.3", "v1.0.0-rc.2"} {
		v, err := ParseVersion(s)
		if err != nil || strings.TrimPrefix(s, "v") != v.String() {
			t.Errorf("%s -> %v, %v", s, v, err)
		}
	}
	for _, s := range []string{"", "1.2", "v1.2.3.4", "latest", "1.2.x"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("%q should not parse", s)
		}
	}
}

var day = time.Date(2026, 11, 9, 15, 4, 5, 0, time.UTC)

func TestRenderSection(t *testing.T) {
	commits := cs(
		"fix(tui): second fix",
		"feat: first feature",
		"docs: not listed",
		"chore(deps): not listed",
		"Merge pull request #1",
		"perf(rules): faster match",
		"revert: undo a thing",
		"feat(cli)!: drop the old flag",
		"feat: second feature",
	)
	got := RenderSection(Version{Major: 1, Minor: 1}, day, commits)
	want := `## [1.1.0] - 2026-11-09

### Changed (BREAKING)
- **cli:** drop the old flag

### Added
- first feature
- second feature

### Changed
- **rules:** faster match
- undo a thing

### Fixed
- **tui:** second fix
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderSectionNothingListed(t *testing.T) {
	got := RenderSection(Version{Major: 1}, day, cs("chore: a"))
	if got != "## [1.0.0] - 2026-11-09\n" {
		t.Errorf("got %q", got)
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs (run: go test ./internal/release -update)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenRealisticSection(t *testing.T) {
	log := "" + // newest first, as git log prints it
		"c9\x1ffix(update): keep the old binary when the checksum differs\x1f\x1e" +
		"c8\x1fMerge pull request #12 from feature/x\x1f\x1e" +
		"c7\x1fdocs: describe the update flow\x1f\x1e" +
		"c6\x1fperf(tui): redraw only changed rows\x1f\x1e" +
		"c5\x1frefactor(config)!: rename user_token_name\x1fBumps nothing but the key.\n\nBREAKING CHANGE: the old key is ignored.\n\x1e" +
		"c4\x1ffeat(doctor): report the token source\x1f\x1e" +
		"c3\x1fchore: bump dependencies\x1f\x1e" +
		"c2\x1ffeat: add the --outdated flag\x1f\x1e" +
		"c1\x1frevert: bring back the footer hint\x1f\x1e"
	commits, err := CommitsSince(func(...string) (string, error) { return log, nil }, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	next, _, ok := NextVersion(Version{Major: 1}, commits)
	if !ok || next.String() != "2.0.0" {
		t.Fatalf("next = %s ok=%v", next, ok)
	}
	checkGolden(t, "section.golden", RenderSection(next, day, commits))
}

const existing = `# Changelog

Intro text.

## [Unreleased]

### Added
- hand written

## [1.0.0] - 2026-11-09

### Added
- old
`

const newSection = "## [1.1.0] - 2026-12-01\n\n### Fixed\n- thing\n"

func TestUpdateChangelog(t *testing.T) {
	t.Run("keeps Unreleased and inserts above the newest release", func(t *testing.T) {
		got, err := UpdateChangelog(existing, newSection, false)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(existing, "## [1.0.0]", newSection+"\n## [1.0.0]", 1)
		if got != want {
			t.Errorf("got:\n%s", got)
		}
	})
	t.Run("replaces Unreleased on request", func(t *testing.T) {
		got, err := UpdateChangelog(existing, newSection, true)
		if err != nil {
			t.Fatal(err)
		}
		want := "# Changelog\n\nIntro text.\n\n" + newSection + "\n## [1.0.0] - 2026-11-09\n\n### Added\n- old\n"
		if got != want {
			t.Errorf("got:\n%s", got)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		once, _ := UpdateChangelog(existing, newSection, false)
		if _, err := UpdateChangelog(once, newSection, false); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("second run err = %v", err)
		}
	})
	t.Run("no release heading appends", func(t *testing.T) {
		got, err := UpdateChangelog("# Changelog\n\n## [Unreleased]\n", newSection, false)
		if err != nil {
			t.Fatal(err)
		}
		if got != "# Changelog\n\n## [Unreleased]\n\n"+newSection {
			t.Errorf("got %q", got)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		got, _ := UpdateChangelog("", newSection, false)
		if got != newSection {
			t.Errorf("got %q", got)
		}
	})
	t.Run("bad section", func(t *testing.T) {
		if _, err := UpdateChangelog(existing, "oops\n", false); err == nil {
			t.Error("want error")
		}
	})
}

func TestDue(t *testing.T) {
	last := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		now  time.Time
		due  bool
		left time.Duration
	}{
		{"before", last.Add(time.Hour), false, time.Hour},
		{"one second before", last.Add(DefaultGrace - time.Second), false, time.Second},
		{"exactly at grace", last.Add(DefaultGrace), true, 0},
		{"after", last.Add(DefaultGrace + time.Minute), true, 0},
		{"clock before activity", last.Add(-time.Minute), false, DefaultGrace + time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			due, left := Due(tt.now, last, DefaultGrace)
			if due != tt.due || left != tt.left {
				t.Errorf("got %v %v, want %v %v", due, left, tt.due, tt.left)
			}
		})
	}
	if DefaultGrace != 2*time.Hour {
		t.Error("DefaultGrace changed; update docs/releasing.md")
	}
}

func fake(out string, err error, got *[]string) Runner {
	return func(args ...string) (string, error) {
		*got = args
		return out, err
	}
}

func TestLastTag(t *testing.T) {
	var args []string
	tag, err := LastTag(fake("v1.0.0\n", nil, &args))
	if err != nil || tag != "v1.0.0" {
		t.Fatalf("%q %v", tag, err)
	}
	if strings.Join(args, " ") != "describe --tags --abbrev=0 --match v[0-9]*" {
		t.Errorf("args = %v", args)
	}
	tag, err = LastTag(fake("", errors.New("fatal: No names found, cannot describe anything."), &args))
	if err != nil || tag != "" {
		t.Errorf("no tag: %q %v", tag, err)
	}
	if _, err = LastTag(fake("", errors.New("fatal: not a git repository"), &args)); err == nil {
		t.Error("other errors must propagate")
	}
}

func TestCommitsSince(t *testing.T) {
	var args []string
	out := "aaaaaaaaaaaa\x1ffeat: newer\x1fbody\x1e\nbbbbbbbbbbbb\x1ffix: older\x1f\x1e\n"
	commits, err := CommitsSince(fake(out, nil, &args), "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "log --format=%H%x1f%s%x1f%b%x1e v1.0.0..HEAD" {
		t.Errorf("args = %v", args)
	}
	if len(commits) != 2 || commits[0].Description != "older" || commits[1].Description != "newer" {
		t.Fatalf("want oldest first, got %+v", commits)
	}
	if commits[0].Hash != "bbbbbbb" {
		t.Errorf("hash = %q", commits[0].Hash)
	}
	if _, err = CommitsSince(fake("", nil, &args), ""); err != nil || args[len(args)-1] != "HEAD" {
		t.Errorf("no tag must log all of HEAD: %v %v", args, err)
	}
	if _, err = CommitsSince(fake("", errors.New("boom"), &args), ""); err == nil {
		t.Error("error must propagate")
	}
}
