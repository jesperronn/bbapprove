package release

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultGrace is how long the repository has to stay quiet before an automatic release is due. This is the one
// knob to change.
const DefaultGrace = 2 * time.Hour

// Due reports whether the quiet period has passed (now - lastActivity >= grace) and how long is left (zero when due).
func Due(now, lastActivity time.Time, grace time.Duration) (bool, time.Duration) {
	left := grace - now.Sub(lastActivity)
	if left <= 0 {
		return true, 0
	}
	return false, left
}

// Runner runs git with the given arguments and returns its standard output. Tests inject a fake.
type Runner func(args ...string) (string, error)

// ExecRunner runs the real git in dir (empty means the current directory). A failure carries git's stderr.
func ExecRunner(dir string) Runner {
	return func(args ...string) (string, error) {
		if dir != "" {
			args = append([]string{"-C", dir}, args...)
		}
		cmd := exec.Command("git", args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}
}

// LastTag returns the newest v* tag reachable from HEAD, or "" when the repository has no such tag yet.
func LastTag(run Runner) (string, error) {
	out, err := run("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*")
	if err != nil {
		if strings.Contains(err.Error(), "No names found") || strings.Contains(err.Error(), "No tags can describe") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitsSince returns the parsed commits in TAG..HEAD, oldest first (all of HEAD when tag is empty).
func CommitsSince(run Runner, tag string) ([]Commit, error) {
	rng := "HEAD"
	if tag != "" {
		rng = tag + "..HEAD"
	}
	out, err := run("log", "--format=%H%x1f%s%x1f%b%x1e", rng)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 3)
		for len(f) < 3 {
			f = append(f, "")
		}
		c := ParseCommit(f[1], f[2])
		c.Hash = f[0]
		if len(c.Hash) > 7 {
			c.Hash = c.Hash[:7]
		}
		commits = append(commits, c)
	}
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 { // git log is newest first
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}
