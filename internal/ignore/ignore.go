// Package ignore stores a per-user, permanent list of dependency names that the
// bbapprove TUI should hide. The list is a plain text file with one dependency
// per line; lines starting with '#' and blank lines are comments.
package ignore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"bbapprove/internal/userconfig"
)

// DefaultPath returns the ignore list next to the config file: $XDG_CONFIG_HOME/bbapprove/ignore when
// XDG_CONFIG_HOME is set, else %AppData%\bbapprove\ignore on Windows, else ~/.config/bbapprove/ignore.
// If no such directory can be determined it returns "", which makes Load, Add and Remove fail.
func DefaultPath() string { return userconfig.Path(os.Getenv, "ignore") }

// normalize trims and lowercases a dependency name or file line.
func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// isEntry reports whether a normalized line is an actual dependency entry
// (not blank and not a comment).
func isEntry(normalized string) bool {
	return normalized != "" && !strings.HasPrefix(normalized, "#")
}

// parseEntries returns the unique, normalized entries in file order.
func parseEntries(content string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		e := normalize(line)
		if !isEntry(e) || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// Load returns the dependency entries stored at path. A missing file is not an
// error and yields an empty list.
func Load(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ignore list %s: %w", path, err)
	}
	return parseEntries(string(data)), nil
}

// Add appends dep to the ignore list at path unless it is already present.
// Existing content (comments, ordering) is left untouched. The file and its
// parent directories are created when missing, and the write is atomic.
func Add(path, dep string) error {
	d := normalize(dep)
	if d == "" {
		return errors.New("ignore: empty dependency name")
	}
	if strings.ContainsAny(d, "\r\n") {
		return fmt.Errorf("ignore: dependency name %q must be a single line", dep)
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read ignore list %s: %w", path, err)
	}
	content := string(data)
	for _, e := range parseEntries(content) {
		if e == d {
			return nil
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += d + "\n"
	return writeAtomic(path, []byte(content), 0o600)
}

// Remove deletes every entry equal to dep (case-insensitive) from the ignore
// list at path. Comments and all other lines are preserved byte for byte. A
// missing file or missing entry is not an error.
func Remove(path, dep string) error {
	d := normalize(dep)
	if d == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read ignore list %s: %w", path, err)
	}
	var b strings.Builder
	removed := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		e := normalize(line)
		if isEntry(e) && e == d {
			removed = true
			continue
		}
		b.WriteString(line)
	}
	if !removed {
		return nil
	}
	return writeAtomic(path, []byte(b.String()), 0o600)
}

// writeAtomic writes data to a temp file in the same directory and renames it
// over path, so a crash never leaves a truncated ignore list behind. Parent
// directories are created with 0700. An existing file keeps its permissions;
// a new file gets defaultMode.
func writeAtomic(path string, data []byte, defaultMode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	mode := defaultMode
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	ok = true
	return nil
}

// titleRE matches Renovate-style dependency update titles, with an optional
// conventional-commit prefix scoped to deps. Group 1 captures the name of an
// "update" title, group 2 the name of a "bump ... from ... to" title.
var titleRE = regexp.MustCompile(
	`(?i)^(?:[a-z]+\(deps\)!?:\s*)?` +
		`(?:update\s+(?:dependency\s+)?(\S+?)(?:\s+(?:docker\s+digest|docker\s+tag|action))?\s+to\s` +
		`|bump\s+(\S+)\s+from\s)`,
)

// DependencyFromTitle extracts the dependency name from a Renovate PR title,
// e.g. "chore(deps): update dependency @biomejs/biome to v2.6.5 (master)"
// yields "@biomejs/biome". It returns ("", false) for titles it does not
// recognize.
func DependencyFromTitle(title string) (string, bool) {
	m := titleRE.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return "", false
	}
	name := m[1]
	if name == "" {
		name = m[2]
	}
	// "update dependency to v1" has no name; the regex would otherwise take the
	// keyword "dependency" as the name.
	if name == "" || strings.EqualFold(name, "dependency") {
		return "", false
	}
	return name, true
}

// Matches returns the first entry that is a case-insensitive substring of
// title, mirroring the semantics of the disallow list. Blank entries never match.
func Matches(entries []string, title string) (string, bool) {
	lt := strings.ToLower(title)
	for _, e := range entries {
		n := normalize(e)
		if n == "" {
			continue
		}
		if strings.Contains(lt, n) {
			return e, true
		}
	}
	return "", false
}
