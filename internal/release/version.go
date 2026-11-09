package release

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Bump is how far a set of commits moves the version.
type Bump int

const (
	BumpNone Bump = iota
	BumpPatch
	BumpMinor
	BumpMajor
)

func (b Bump) String() string {
	return [...]string{"none", "patch", "minor", "major"}[b]
}

// Version is a semver version. Pre holds the pre-release part (for example "rc.2"), empty for a stable release.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// ParseVersion accepts "1.2.3", "v1.2.3" and "v1.2.3-rc.1".
func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("not a semver version: %q", s)
	}
	n := func(x string) int { v, _ := strconv.Atoi(x); return v }
	return Version{n(m[1]), n(m[2]), n(m[3]), m[4]}, nil
}

// String is "1.2.3" or "1.2.3-rc.1" (no leading v; the changelog headings have none).
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Tag is the git tag name, "v" plus String.
func (v Version) Tag() string { return "v" + v.String() }

// BumpOf is the largest bump the commits call for: breaking is major, feat is minor, fix and perf are patch,
// everything else (docs, chore, ...) is none.
func BumpOf(commits []Commit) Bump {
	b := BumpNone
	for _, c := range commits {
		x := BumpNone
		switch {
		case c.Type == "" && !c.Breaking:
		case c.Breaking:
			x = BumpMajor
		case c.Type == "feat":
			x = BumpMinor
		case c.Type == "fix" || c.Type == "perf":
			x = BumpPatch
		}
		if x > b {
			b = x
		}
	}
	return b
}

// NextVersion computes the version after last. Rules:
//   - Plain semver, also below 1.0: breaking is major (0.3.1 becomes 1.0.0), feat minor, fix and perf patch.
//   - Nothing releasable (only docs, chore, test, ... commits) returns ok=false.
//   - When last is a pre-release (v1.0.0-rc.2) the next version is its stable core (1.0.0) whatever the bump:
//     the release candidate already announced that version. Commits still have to be releasable.
//   - The zero Version (no tag yet) bumps from 0.0.0.
func NextVersion(last Version, commits []Commit) (next Version, bump Bump, ok bool) {
	bump = BumpOf(commits)
	if bump == BumpNone {
		return last, bump, false
	}
	if last.Pre != "" {
		return Version{Major: last.Major, Minor: last.Minor, Patch: last.Patch}, bump, true
	}
	next = last
	switch bump {
	case BumpMajor:
		next = Version{Major: last.Major + 1}
	case BumpMinor:
		next = Version{Major: last.Major, Minor: last.Minor + 1}
	case BumpPatch:
		next.Patch++
	}
	return next, bump, true
}
