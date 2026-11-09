// Package update checks for newer bbapprove releases and installs them safely.
//
// Design: a read-only check, a printed plan, an explicit confirmation and, for installs without a
// checkout, a SIDE-BY-SIDE install into a new sibling directory that never modifies the running tree.
// Nothing here runs unless the user passes --outdated or --update: bbapprove never phones home.
package update

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed SemVer 2.0 version. Build metadata is ignored.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string // pre-release identifiers, nil for a stable release
}

// IsPre reports whether v is a pre-release.
func (v Version) IsPre() bool { return len(v.Pre) > 0 }

func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.IsPre() {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

var idRe = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// ParseVersion parses "1.2.3", "v1.2.3-rc.1" or "1.2.3-rc.1+build".
func ParseVersion(s string) (Version, error) {
	var v Version
	orig := s
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+") // build metadata does not take part in precedence
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("%q is not a semantic version", orig)
	}
	nums := []*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || p == "" || p[0] == '+' {
			return v, fmt.Errorf("%q is not a semantic version", orig)
		}
		*nums[i] = n
	}
	if hasPre {
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if !idRe.MatchString(id) {
				return Version{}, fmt.Errorf("%q has an invalid pre-release part", orig)
			}
		}
	}
	return v, nil
}

// Compare returns -1, 0 or 1 by SemVer 2.0 precedence (1.0.0-rc.2 < 1.0.0-rc.10 < 1.0.0).
func Compare(a, b Version) int {
	for _, p := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case !a.IsPre() && !b.IsPre():
		return 0
	case !a.IsPre():
		return 1 // a release outranks its own pre-releases
	case !b.IsPre():
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := compareID(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

func isNum(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func compareID(a, b string) int {
	an, bn := isNum(a), isNum(b)
	switch {
	case an && bn:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0") // arbitrary length, no overflow
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1 // numeric identifiers rank below alphanumeric ones
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

// Current describes the running build's version string.
type Current struct {
	Raw   string
	Known bool    // Base is valid; false for "dev" or anything unparsable
	Base  Version // the release tag the build is based on
	Ahead int     // commits after Base (git-describe dev builds)
	Dirty bool
	Dev   bool // a git-describe build between releases, or a dirty one
}

var describeRe = regexp.MustCompile(`^(.+?)-(\d+)-g[0-9a-f]{4,40}(-dirty)?$`)

// ParseCurrent understands "v1.0.0-rc.2", "v1.0.0-rc.2-4-gabc1234[-dirty]", "v1.0.0-dirty" and "dev".
func ParseCurrent(s string) Current {
	c := Current{Raw: s}
	s = strings.TrimSpace(s)
	base := s
	if m := describeRe.FindStringSubmatch(s); m != nil {
		base = m[1]
		c.Ahead, _ = strconv.Atoi(m[2])
		c.Dirty = m[3] != ""
		c.Dev = true
	} else if strings.HasSuffix(s, "-dirty") {
		base = strings.TrimSuffix(s, "-dirty")
		c.Dirty, c.Dev = true, true
	}
	if v, err := ParseVersion(base); err == nil {
		c.Base, c.Known = v, true
	}
	return c
}
