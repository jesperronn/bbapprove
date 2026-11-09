package release

import (
	"fmt"
	"strings"
)

// UpdateChangelog inserts section (as produced by RenderSection) directly above the newest released "## [" heading,
// keeping the header text. A hand-written "## [Unreleased]" block is left where it is and the new section goes
// after it; with replaceUnreleased the block is removed instead (no empty one is left behind). Without any release
// heading the section is appended. It returns an error when the section's version heading already exists, so a
// second run changes nothing.
func UpdateChangelog(existing, section string, replaceUnreleased bool) (string, error) {
	first, _, _ := strings.Cut(section, "\n")
	if !strings.HasPrefix(first, "## [") || !strings.Contains(first, "]") {
		return "", fmt.Errorf("section does not start with a \"## [VERSION]\" heading")
	}
	heading := first[:strings.Index(first, "]")+1]
	section = strings.TrimRight(section, "\n") + "\n"

	lines := strings.SplitAfter(existing, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	isHeading := func(l string) bool { return strings.HasPrefix(l, "## [") }
	unrel := -1 // index of the Unreleased heading
	newest := -1
	for i, l := range lines {
		if !isHeading(l) {
			continue
		}
		if strings.HasPrefix(l, heading) {
			return "", fmt.Errorf("%s already exists in the changelog", heading)
		}
		if strings.HasPrefix(l, "## [Unreleased]") {
			if unrel < 0 {
				unrel = i
			}
		} else if newest < 0 {
			newest = i
		}
	}

	if replaceUnreleased && unrel >= 0 {
		end := len(lines)
		for i := unrel + 1; i < len(lines); i++ {
			if isHeading(lines[i]) {
				end = i
				break
			}
		}
		lines = append(lines[:unrel:unrel], lines[end:]...)
		if newest >= 0 && newest > unrel {
			newest -= end - unrel
		}
	}

	at := len(lines)
	if newest >= 0 {
		at = newest
	}
	head := strings.Join(lines[:at], "")
	tail := strings.Join(lines[at:], "")
	if head != "" && !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	if head != "" && !strings.HasSuffix(head, "\n\n") {
		head += "\n"
	}
	if tail != "" {
		section += "\n"
	}
	return head + section + tail, nil
}
