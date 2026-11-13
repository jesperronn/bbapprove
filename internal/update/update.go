package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"bbapprove/internal/style"
)

// DefaultSlug is the project's own home; override with BBAPPROVE_UPDATE_REPO or config update_repo.
const DefaultSlug = "jesperronn/bbapprove"

// Cmd is one external command. With a nil Stdout the output is captured and returned; otherwise it is
// streamed to Stdout/Stderr.
type Cmd struct {
	Dir            string
	Name           string
	Args           []string
	Stdout, Stderr io.Writer
}

// Runner executes external commands (gh, git, bin/build). Tests inject a fake; there is no shell.
type Runner interface {
	Run(ctx context.Context, c Cmd) ([]byte, error)
}

// ExecRunner runs commands for real via os/exec (no shell string).
type ExecRunner struct{}

// Run implements Runner. Captured runs never prompt (GH_PROMPT_DISABLED, GIT_TERMINAL_PROMPT=0) and
// put the command's stderr into the error. A missing program yields an error wrapping exec.ErrNotFound.
func (ExecRunner) Run(ctx context.Context, c Cmd) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0")
	if c.Stdout != nil {
		cmd.Stdout, cmd.Stderr = c.Stdout, c.Stderr
		return nil, cmd.Run()
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(errb.String()); s != "" {
			return out.Bytes(), fmt.Errorf("%w: %s", err, style.OneLine(s))
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// Options carries everything the process would otherwise provide, so tests need no real exec, network
// or clock.
type Options struct {
	Runner      Runner
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	IsTTY       bool             // stdin and stdout are terminals (the confirmation prompt is possible)
	Now         func() time.Time // names scratch directories; defaults to time.Now
	GOOS        string           // defaults to runtime.GOOS (set by the caller)
	GOARCH      string
	InstallRoot string        // the running install: parent of the binary's directory, symlinks resolved
	ExeDir      string        // Windows: the folder holding the running bbapprove.exe; defaults to the running executable's
	Version     string        // the embedded build version
	Slug        string        // owner/repo, default DefaultSlug
	PathEnv     string        // $PATH, to find an installed symlink; defaults to os.Getenv("PATH")
	Pre         bool          // --pre: also offer pre-releases to a stable build
	Yes         bool          // --yes: skip the confirmation
	Timeout     time.Duration // per network lookup; default 10s
	MaxExtract  int64         // cap on extracted bytes; default 200 MiB
	// OutTheme paints what goes to Stdout, ErrTheme what goes to Stderr. The zero value is plain text; each
	// stream has its own theme because only one of them may be a terminal.
	OutTheme, ErrTheme style.Theme

	rename func(oldpath, newpath string) error // tests inject failures; defaults to os.Rename
}

// outln and errln print markup (see package style) with escaped arguments to the respective stream.
func (o Options) outln(markup string, a ...any) {
	fmt.Fprintln(o.Stdout, o.OutTheme.Sprintf(markup, a...))
}

func (o Options) errln(markup string, a ...any) {
	fmt.Fprintln(o.Stderr, o.ErrTheme.Sprintf(markup, a...))
}

// failln prints "bbapprove: <message>" with the prefix painted as an error; the message is data.
func (o Options) failln(format string, a ...any) {
	o.errln("{fail:bbapprove:} %s", fmt.Sprintf(format, a...))
}

func (o *Options) fill() {
	if o.Slug == "" {
		o.Slug = DefaultSlug
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MaxExtract == 0 {
		o.MaxExtract = 200 << 20
	}
	if o.PathEnv == "" {
		o.PathEnv = os.Getenv("PATH")
	}
	if o.Runner == nil {
		o.Runner = ExecRunner{}
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
}

// Install types.
const (
	Checkout = "checkout"
	Archive  = "archive"
)

// DetectInstall says whether root is a git checkout (has .git: directory, or file for a worktree) or an
// unpacked release archive.
func DetectInstall(root string) string {
	if _, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		return Checkout
	}
	return Archive
}

var slugRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// Release is one published version.
type Release struct {
	Tag string
	Pre bool
	Ver Version
}

// Result is the outcome of a check.
type Result struct {
	Current  Current
	Latest   Release
	Outdated bool
	Install  string // Checkout or Archive
	Source   string // "gh" or "git tags"
}

func (o Options) releasesURL() string { return "https://github.com/" + o.Slug + "/releases" }

func (o Options) capture(ctx context.Context, c Cmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	return o.Runner.Run(ctx, c)
}

// Check compares the running version with the newest release. It only reads: gh release list, or in a
// checkout git ls-remote as a fallback. The error is a "could not check" (exit 2) condition.
func Check(ctx context.Context, o Options) (*Result, error) {
	o.fill()
	return check(ctx, o)
}

func check(ctx context.Context, o Options) (*Result, error) {
	if !slugRe.MatchString(o.Slug) {
		return nil, fmt.Errorf("invalid update repository %q (want owner/repo)", o.Slug)
	}
	cur := ParseCurrent(o.Version)
	res := &Result{Current: cur, Install: DetectInstall(o.InstallRoot)}
	rels, source, err := o.releases(ctx, res.Install)
	if err != nil {
		return nil, err
	}
	res.Source = source
	// Policy: a pre-release build tracks pre-releases too; a stable build only sees stable ones unless --pre.
	includePre := o.Pre || (cur.Known && cur.Base.IsPre())
	var best *Release
	for i := range rels {
		r := rels[i]
		if r.Pre && !includePre {
			continue
		}
		if best == nil || Compare(r.Ver, best.Ver) > 0 {
			best = &r
		}
	}
	if best == nil {
		kind := "stable "
		if includePre {
			kind = ""
		}
		return nil, fmt.Errorf("no %sreleases found for %s (see %s)", kind, o.Slug, o.releasesURL())
	}
	res.Latest = *best
	if !cur.Known {
		return res, fmt.Errorf("current version %q is not a release, cannot compare it with latest %s (build with bin/build to embed a version)", o.Version, best.Tag)
	}
	res.Outdated = Compare(cur.Base, best.Ver) < 0
	return res, nil
}

type ghRelease struct {
	TagName      string `json:"tagName"`
	IsPrerelease bool   `json:"isPrerelease"`
	IsDraft      bool   `json:"isDraft"`
}

func (o Options) releases(ctx context.Context, install string) ([]Release, string, error) {
	out, ghErr := o.capture(ctx, Cmd{Name: "gh", Args: []string{"release", "list", "--repo", o.Slug,
		"--limit", "30", "--json", "tagName,isPrerelease,isDraft,publishedAt"}})
	if ghErr == nil {
		var list []ghRelease
		if err := json.Unmarshal(out, &list); err != nil {
			ghErr = fmt.Errorf("unexpected gh output: %v", err)
		} else {
			var rels []Release
			for _, g := range list {
				if g.IsDraft {
					continue
				}
				if v, err := ParseVersion(g.TagName); err == nil && safeTag(g.TagName) {
					rels = append(rels, Release{Tag: g.TagName, Pre: g.IsPrerelease || v.IsPre(), Ver: v})
				}
			}
			return rels, "gh", nil
		}
	}
	if install == Checkout {
		out, err := o.capture(ctx, Cmd{Dir: o.InstallRoot, Name: "git", Args: []string{"ls-remote", "--tags", "--refs", "origin"}})
		if err == nil {
			return parseLsRemote(string(out)), "git tags", nil
		}
		return nil, "", fmt.Errorf("cannot check for releases: gh failed (%v) and git ls-remote failed (%v); see %s", ghErr, err, o.releasesURL())
	}
	return nil, "", fmt.Errorf("cannot check for releases: %w; install the GitHub CLI (gh, https://cli.github.com) and run `gh auth login`, or see %s", ghErr, o.releasesURL())
}

var tagRe = regexp.MustCompile(`^[0-9A-Za-z._+-]+$`)

// safeTag guards values that end up in arguments and directory names.
func safeTag(t string) bool { return tagRe.MatchString(t) && !strings.HasPrefix(t, "-") }

func parseLsRemote(s string) []Release {
	var rels []Release
	for _, line := range strings.Split(s, "\n") {
		_, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		tag, found := strings.CutPrefix(ref, "refs/tags/")
		if !ok || !found || !safeTag(tag) {
			continue
		}
		if v, err := ParseVersion(tag); err == nil {
			rels = append(rels, Release{Tag: tag, Pre: v.IsPre(), Ver: v})
		}
	}
	return rels
}

// describeCurrent is the running version as markup (versions highlighted); outdated text is data.
func (o Options) describeCurrent(c Current) string {
	if c.Dev && c.Known {
		s := o.OutTheme.Sprintf("bbapprove {ver:%s} is a development build (base {ver:%s}, %s commits ahead", c.Raw, c.Base, fmt.Sprint(c.Ahead))
		if c.Dirty {
			s += o.OutTheme.Sprintf(", {warn:uncommitted changes}")
		}
		return s + ")"
	}
	return o.OutTheme.Sprintf("bbapprove {ver:%s}", c.Raw)
}

func (o Options) latestText(r Release) string {
	s := o.OutTheme.Sprintf("latest {ver:%s}", r.Tag)
	if r.Pre {
		s += o.OutTheme.Sprintf(" {dim:(pre-release)}")
	}
	return s
}

// Outdated implements --outdated. Exit codes: 0 up to date, 1 outdated, 2 could not check.
func Outdated(ctx context.Context, o Options) int {
	o.fill()
	res, err := check(ctx, o)
	if err != nil {
		o.failln("%v", err)
		return 2
	}
	o.printCheck(res)
	if res.Outdated {
		return 1
	}
	return 0
}

func (o Options) printCheck(res *Result) {
	cur := o.describeCurrent(res.Current)
	if !res.Outdated {
		if res.Current.Dev {
			fmt.Fprintf(o.Stdout, "%s — %s: %s\n", cur, o.latestText(res.Latest), o.OutTheme.Paint(style.OK, "up to date"))
		} else {
			o.outln("bbapprove {ver:%s} is {ok:up to date}", res.Current.Raw)
		}
		return
	}
	fmt.Fprintf(o.Stdout, "%s — %s: %s\n", cur, o.latestText(res.Latest), o.OutTheme.Paint(style.Warn, "update available"))
	if res.Install == Checkout {
		o.outln("  update with: bbapprove {flag:--update}   (or: {cmd:git pull --ff-only --tags && bin/build})")
	} else {
		o.outln("  update with: bbapprove {flag:--update}   (release: {path:%s/tag/%s})", o.releasesURL(), res.Latest.Tag)
	}
}

// confirm asks "Proceed? [y/N]"; anything but y/yes (including EOF) is no.
func (o Options) confirm() bool {
	fmt.Fprint(o.Stdout, o.OutTheme.Sprintf("{h:Proceed?} {dim:[y/N]} "))
	if o.Stdin == nil {
		fmt.Fprintln(o.Stdout)
		return false
	}
	line, _ := bufio.NewReader(o.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// proceed handles the Yes / TTY / prompt decision after the plan was printed. ok=false means exit with code.
func (o Options) proceed() (ok bool, code int) {
	switch {
	case o.Yes:
		return true, 0
	case !o.IsTTY:
		o.errln("{fail:bbapprove:} not a terminal, nothing changed; re-run with {flag:--yes} to apply")
		return false, 2
	case o.confirm():
		return true, 0
	}
	o.outln("{warn:Aborted, nothing changed.}")
	return false, 1
}

// Update implements --update: the same check as --outdated, then a plan, a confirmation and the install.
func Update(ctx context.Context, o Options) int {
	o.fill()
	res, err := check(ctx, o)
	if err != nil {
		o.failln("%v", err)
		if o.Install() == Archive && errors.Is(err, exec.ErrNotFound) {
			o.manualSteps("<tag>")
		}
		return 2
	}
	o.printCheck(res)
	if !res.Outdated {
		return 0
	}
	if res.Install == Checkout {
		return o.updateCheckout(ctx, res)
	}
	return o.updateArchive(ctx, res)
}

// Install is the install type of o.InstallRoot.
func (o Options) Install() string { return DetectInstall(o.InstallRoot) }
