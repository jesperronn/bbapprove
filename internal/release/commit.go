// Package release turns conventional-commit subjects since the last tag into the next version number and a
// Keep a Changelog section. It never touches the network; git is reached only through an injected Runner.
package release

import (
	"regexp"
	"strings"
)

// Commit is one parsed commit. Type is empty for a subject that is not a conventional commit (a merge commit,
// stray text); such commits are kept but never listed and never cause a bump.
type Commit struct {
	Type        string
	Scope       string
	Breaking    bool
	Description string
	Hash        string // short hash; kept for tooling, never rendered into the changelog
}

var subjectRe = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^()]*)\))?(!)?:[ \t]+(\S.*)$`)

// ParseCommit parses a commit subject and body. A commit is breaking when the subject has "!" before the colon
// or the body has a line starting "BREAKING CHANGE:" or "BREAKING-CHANGE:". The type is lower-cased.
func ParseCommit(subject, body string) Commit {
	c := Commit{}
	m := subjectRe.FindStringSubmatch(strings.TrimSpace(subject))
	if m == nil {
		return c
	}
	c.Type = strings.ToLower(m[1])
	c.Scope = strings.TrimSpace(m[2])
	c.Breaking = m[3] == "!"
	c.Description = strings.TrimSpace(m[4])
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BREAKING CHANGE:") || strings.HasPrefix(line, "BREAKING-CHANGE:") {
			c.Breaking = true
		}
	}
	return c
}
