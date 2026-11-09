package release

import (
	"fmt"
	"strings"
	"time"
)

// BreakingHeading is the section that lists every breaking commit, whatever its type. It always comes first.
const BreakingHeading = "Changed (BREAKING)"

// SectionOf maps a commit type to its Keep a Changelog heading. This is the one table to edit to change what is
// listed: a type that is not here (docs, test, chore, refactor, ci, build, style) never appears in the changelog.
// Headings are rendered in the order of SectionOrder.
var SectionOf = map[string]string{
	"feat":   "Added",
	"fix":    "Fixed",
	"perf":   "Changed",
	"revert": "Changed",
}

// SectionOrder is the order of the headings after BreakingHeading.
var SectionOrder = []string{"Added", "Changed", "Fixed"}

// Entry renders one changelog line: "- **scope:** description", the scope omitted when empty. The description is
// used as written (no case or full stop changes). The short hash is deliberately left out: it is noise for users.
func Entry(c Commit) string {
	if c.Scope == "" {
		return "- " + c.Description
	}
	return fmt.Sprintf("- **%s:** %s", c.Scope, c.Description)
}

// Entries groups the listed commits by heading, in the order the headings are rendered. Commit order is kept
// (the caller passes oldest first). A breaking commit is listed once, under BreakingHeading.
func Entries(commits []Commit) (headings []string, byHeading map[string][]string) {
	byHeading = map[string][]string{}
	for _, c := range commits {
		if c.Type == "" {
			continue
		}
		h := SectionOf[c.Type]
		if c.Breaking {
			h = BreakingHeading
		}
		if h == "" {
			continue
		}
		byHeading[h] = append(byHeading[h], Entry(c))
	}
	for _, h := range append([]string{BreakingHeading}, SectionOrder...) {
		if len(byHeading[h]) > 0 {
			headings = append(headings, h)
		}
	}
	return headings, byHeading
}

// RenderSection renders "## [X.Y.Z] - YYYY-MM-DD" and the sections, ending with a newline. Commits are expected
// oldest first. With nothing to list it returns only the heading.
func RenderSection(version Version, date time.Time, commits []Commit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## [%s] - %s\n", version, date.Format("2006-01-02"))
	headings, by := Entries(commits)
	for _, h := range headings {
		fmt.Fprintf(&b, "\n### %s\n", h)
		for _, e := range by[h] {
			b.WriteString(e + "\n")
		}
	}
	return b.String()
}
