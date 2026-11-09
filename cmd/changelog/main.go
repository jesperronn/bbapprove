// Command changelog is a maintainer tool (not shipped in the release archives): it derives the next version and the
// CHANGELOG section from the conventional-commit subjects since the last tag. See docs/releasing.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bbapprove/internal/release"
)

// Exit codes.
const (
	exitOK         = 0
	exitError      = 1
	exitUsage      = 2
	exitNothing    = 3 // next/render/update: nothing releasable
	exitNotYetDue  = 4 // due: the quiet period has not passed
	dateLayout     = "2006-01-02"
	topUsage       = "Usage: changelog <next|render|update|due> [flags]   (changelog SUBCOMMAND -h for flags)\n"
	topUsageDetail = `
  next     print the next version (exit 0 releasable, 3 nothing releasable)
  render   print the CHANGELOG section for the next version
  update   insert that section into CHANGELOG.md
  due      print the time left of the quiet period (exit 0 due, 4 not yet)

Common flags: --repo DIR (git repository, default .). Exit: 1 error, 2 usage error.
`
)

type env struct {
	out, err io.Writer
	run      release.Runner // nil: real git in --repo
	now      func() time.Time
}

func main() {
	e := env{out: os.Stdout, err: os.Stderr, now: time.Now}
	os.Exit(e.main(os.Args[1:]))
}

func (e env) main(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.err, topUsage+topUsageDetail)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(e.out, topUsage+topUsageDetail)
		return exitOK
	case "next", "render", "update", "due":
	default:
		fmt.Fprintf(e.err, "changelog: unknown subcommand %q\n%s", args[0], topUsage)
		return exitUsage
	}
	cmd := args[0]
	fs := flag.NewFlagSet("changelog "+cmd, flag.ContinueOnError)
	fs.SetOutput(e.err)
	repo := fs.String("repo", ".", "git repository")
	var version, date, file, grace, lastActivity, nowFlag string
	var replace bool
	switch cmd {
	case "render", "update":
		fs.StringVar(&version, "version", "", "use this version instead of the computed one")
		fs.StringVar(&date, "date", "", "heading date YYYY-MM-DD (default today)")
	}
	if cmd == "update" {
		fs.StringVar(&file, "file", "", "changelog to edit (default REPO/CHANGELOG.md)")
		fs.BoolVar(&replace, "replace-unreleased", false, "drop the hand-written [Unreleased] block instead of keeping it")
	}
	if cmd == "due" {
		fs.StringVar(&grace, "grace", release.DefaultGrace.String(), "quiet period")
		fs.StringVar(&lastActivity, "last-activity", "", "RFC3339 time of the last activity (default: time of the HEAD commit)")
		fs.StringVar(&nowFlag, "now", "", "RFC3339 current time (default: now)")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.err, "changelog %s: unexpected argument %q\n", cmd, fs.Arg(0))
		return exitUsage
	}
	run := e.run
	if run == nil {
		run = release.ExecRunner(*repo)
	}

	if cmd == "due" {
		return e.due(run, grace, lastActivity, nowFlag)
	}

	day := e.now()
	if date != "" {
		var err error
		if day, err = time.Parse(dateLayout, date); err != nil {
			fmt.Fprintf(e.err, "changelog: bad --date %q (want YYYY-MM-DD)\n", date)
			return exitUsage
		}
	}

	tag, err := release.LastTag(run)
	if err != nil {
		return e.fail(err)
	}
	last := release.Version{}
	if tag != "" {
		if last, err = release.ParseVersion(tag); err != nil {
			return e.fail(err)
		}
	}
	commits, err := release.CommitsSince(run, tag)
	if err != nil {
		return e.fail(err)
	}
	next, _, ok := release.NextVersion(last, commits)
	if version != "" {
		if next, err = release.ParseVersion(version); err != nil {
			fmt.Fprintf(e.err, "changelog: %v\n", err)
			return exitUsage
		}
	} else if !ok {
		fmt.Fprintf(e.err, "changelog: nothing releasable since %s\n", orNone(tag))
		return exitNothing
	}
	if cmd == "next" {
		fmt.Fprintln(e.out, next.Tag())
		return exitOK
	}

	if headings, _ := release.Entries(commits); len(headings) == 0 {
		fmt.Fprintf(e.err, "changelog: no listed commits since %s\n", orNone(tag))
		return exitNothing
	}
	section := release.RenderSection(next, day, commits)
	if cmd == "render" {
		fmt.Fprint(e.out, section)
		return exitOK
	}

	if file == "" {
		file = filepath.Join(*repo, "CHANGELOG.md")
	}
	existing, err := os.ReadFile(file)
	if err != nil {
		return e.fail(err)
	}
	updated, err := release.UpdateChangelog(string(existing), section, replace)
	if err != nil {
		return e.fail(err)
	}
	if err := os.WriteFile(file, []byte(updated), 0o644); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.out, "%s: added %s\n", file, next)
	return exitOK
}

func (e env) due(run release.Runner, graceS, lastS, nowS string) int {
	g, err := time.ParseDuration(graceS)
	if err != nil || g < 0 {
		fmt.Fprintf(e.err, "changelog: bad --grace %q (want a duration such as 2h)\n", graceS)
		return exitUsage
	}
	now := e.now()
	if nowS != "" {
		if now, err = time.Parse(time.RFC3339, nowS); err != nil {
			fmt.Fprintf(e.err, "changelog: bad --now %q (want RFC3339)\n", nowS)
			return exitUsage
		}
	}
	var last time.Time
	if lastS != "" {
		if last, err = time.Parse(time.RFC3339, lastS); err != nil {
			fmt.Fprintf(e.err, "changelog: bad --last-activity %q (want RFC3339)\n", lastS)
			return exitUsage
		}
	} else {
		out, err := run("log", "-1", "--format=%cI", "HEAD")
		if err != nil {
			return e.fail(err)
		}
		if last, err = time.Parse(time.RFC3339, strings.TrimSpace(out)); err != nil {
			return e.fail(err)
		}
	}
	due, left := release.Due(now, last, g)
	fmt.Fprintln(e.out, left)
	if !due {
		return exitNotYetDue
	}
	return exitOK
}

func (e env) fail(err error) int {
	fmt.Fprintf(e.err, "changelog: %v\n", err)
	return exitError
}

func orNone(tag string) string {
	if tag == "" {
		return "the first commit"
	}
	return tag
}
