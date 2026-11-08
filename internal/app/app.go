// Package app implements the bbapprove command: dry run and --apply.
package app

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"bbapprove/internal/adapter"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
)

// Options carries everything the process environment would otherwise provide.
type Options struct {
	Args    []string
	Environ []string // KEY=VALUE, like os.Environ()
	RepoDir string   // repo root, used for default adapter and disallow file locations
	Stdout  io.Writer
	Stderr  io.Writer
	IsTTY   bool   // stdout is a terminal
	Width   int    // terminal width; only used when IsTTY
	Version string // build version shown in the help text
	// IgnorePath overrides the per-user ignore file (default: ignore.DefaultPath(), honouring
	// XDG_CONFIG_HOME from Environ first). Mainly for tests.
	IgnorePath string
}

func (o Options) env(key string) string {
	v := ""
	for _, kv := range o.Environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val // last wins
		}
	}
	return v
}

const usageBody = `approve and merge Renovate PRs on Bitbucket Server.

Usage: bbapprove [--apply] [--hide-ignored] [--disallow a,b] [--token T] [PR_URL...]

With no PR_URL, every open PR where you are a reviewer is considered.
Without --apply nothing is changed; eligibility is only reported.
Your per-user ignore list ($XDG_CONFIG_HOME/bbapprove/ignore, default ~/.config/bbapprove/ignore) is
honoured: a PR whose title matches an entry is shown dim as IGNORED and is never approved, merged,
declined or commented, with or without --apply. --hide-ignored leaves those rows out of the table
(the footer then says "N ignored (hidden)").
The STATUS column is blank for an eligible, untouched PR; otherwise SKIP, IGNORED, or the result of
an action (APPROVING… MERGED, NOT MERGED, DECLINING… DECLINED, COMMENTING… COMMENTED, FAIL).
-i opens an interactive TUI: x/space mark, ctrl+a mark all eligible, a approve+merge, d decline,
c comment (cursor row); A/D/C act on the marked rows; i ignore dependency, h or . show/hide ignored
rows, r reload, o open, ? help, q quit (prints a summary of what was done). --no-mouse (or
BBAPPROVE_MOUSE=0) turns mouse capture off.

A PR is eligible when ALL hold:
  - author email is BBAPPROVE_AUTHOR_EMAIL (default renovate-bot@example.com)
  - title starts with "chore(deps):" or "fix(deps):"
  - title does not mention a disallowed dependency (disallow.txt, --disallow)
  - update is minor/patch/digest (title "to vN" only, or branch renovate/major-*, is major)
  - not already approved by you
  - not on your ignore list (otherwise IGNORED)

Env: BBAPPROVE_AUTHOR_EMAIL, BBAPPROVE_DISALLOW, BBAPPROVE_DISALLOW_FILE, BBAPPROVE_JOBS,
     BBAPPROVE_ADAPTER, BBAPPROVE_TOKEN, BBAPPROVE_MOUSE; NO_COLOR, FORCE_COLOR.
`

// usage returns the help text; the first line carries the version when known.
func usage(version string) string {
	if version == "" {
		version = "dev"
	}
	return "bbapprove " + version + " — " + usageBody
}

type printer struct {
	mu sync.Mutex
	w  io.Writer
}

func (p *printer) line(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	io.WriteString(p.w, s+"\n")
}

// Run executes bbapprove and returns the exit code.
func Run(ctx context.Context, o Options) int {
	out := &printer{w: o.Stdout}
	var (
		apply    bool
		hideIgn  bool
		disallow = o.env("BBAPPROVE_DISALLOW")
		token    = o.env("BBAPPROVE_TOKEN")
		targets  []string
	)
	args := o.Args
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() (string, bool) {
			if _, v, ok := strings.Cut(a, "="); ok {
				return v, true
			}
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch {
		case a == "-h" || a == "--help":
			io.WriteString(o.Stdout, usage(o.Version))
			return 0
		case a == "--apply":
			apply = true
		case a == "--hide-ignored":
			hideIgn = true
		case a == "-i" || a == "--interactive":
			fmt.Fprintln(o.Stderr, "interactive mode: not implemented yet")
			return 2
		case a == "--disallow" || strings.HasPrefix(a, "--disallow="):
			v, ok := val()
			if !ok {
				fmt.Fprintln(o.Stderr, "--disallow needs a value")
				return 2
			}
			disallow = v
		case a == "--token" || strings.HasPrefix(a, "--token="):
			v, ok := val()
			if !ok {
				fmt.Fprintln(o.Stderr, "--token needs a value")
				return 2
			}
			token = v
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(o.Stderr, "Unknown option: %s\n%s", a, usage(o.Version))
			return 2
		default:
			targets = append(targets, a)
		}
	}

	email := o.env("BBAPPROVE_AUTHOR_EMAIL")
	if email == "" {
		email = "renovate-bot@example.com"
	}
	file := o.env("BBAPPROVE_DISALLOW_FILE")
	if file == "" {
		file = filepath.Join(o.RepoDir, "disallow.txt")
	}
	adapterPath := o.env("BBAPPROVE_ADAPTER")
	if adapterPath == "" {
		adapterPath = filepath.Join(o.RepoDir, "adapters", "bitbucket")
	}
	jobs := runtime.NumCPU()
	if n, err := strconv.Atoi(o.env("BBAPPROVE_JOBS")); err == nil && n > 0 {
		jobs = n
	}

	ignorePath := o.IgnorePath
	if ignorePath == "" {
		if x := o.env("XDG_CONFIG_HOME"); x != "" {
			ignorePath = filepath.Join(x, "bbapprove", "ignore")
		} else {
			ignorePath = ignore.DefaultPath()
		}
	}
	// Fail closed: an unreadable ignore list must never be treated as empty, or an ignored PR could be merged.
	ignored, err := ignore.Load(ignorePath)
	if err != nil {
		fmt.Fprintf(o.Stderr, "cannot read ignore list: %v\n", err)
		return 1
	}

	cfg := rules.Config{AuthorEmail: email, Disallow: rules.LoadDisallow(file, disallow)}
	client := &adapter.Client{Path: adapterPath, Token: token, Environ: o.Environ}

	if token == "" {
		t, err := client.AuthToken(ctx)
		if err != nil {
			fmt.Fprintln(o.Stderr, err)
			return 1
		}
		client.Token = t
	}

	var prs []adapter.PR
	if len(targets) > 0 {
		var err error
		prs, err = fetchAll(ctx, client, targets, jobs)
		if err != nil {
			fmt.Fprintln(o.Stderr, err)
			return 1
		}
	} else {
		var err error
		prs, err = client.List(ctx)
		if err != nil {
			fmt.Fprintln(o.Stderr, err)
			return 1
		}
	}

	st := Style{Color: color(o)}
	if o.IsTTY {
		st.Width = o.Width
	}
	out.line(st.Header())

	var eligible []adapter.PR
	hidden := 0
	for _, pr := range prs {
		kind, reason := cfg.Classify(ignored, pr)
		switch kind {
		case rules.Eligible:
			eligible = append(eligible, pr)
			if !apply {
				out.line(st.Row("", "", pr, ""))
			}
		case rules.Ignored:
			if hideIgn {
				hidden++
			} else {
				out.line(st.Row("IGNORED", dim, pr, reason))
			}
		default:
			out.line(st.Row("SKIP", yellow, pr, reason))
		}
	}
	if hidden > 0 {
		out.line(fmt.Sprintf("%d ignored (hidden)", hidden))
	}

	if !apply {
		out.line("Dry run — pass --apply to approve and merge.")
		return 0
	}

	forEach(len(eligible), jobs, func(i int) {
		out.line(act(ctx, client, st, eligible[i]))
	})
	return 0
}

func color(o Options) bool {
	if o.env("NO_COLOR") != "" {
		return false
	}
	return o.IsTTY || o.env("FORCE_COLOR") != ""
}

// act approves then merges one PR and returns the single result line (not width-truncated, so errors stay readable).
func act(ctx context.Context, c *adapter.Client, st Style, pr adapter.PR) string {
	if err := c.Approve(ctx, pr.URL); err != nil {
		return st.rowFull("FAIL", red, pr, oneLine(err.Error()))
	}
	if err := c.Merge(ctx, pr.URL); err != nil {
		return st.rowFull("NOT MERGED", red, pr, oneLine(err.Error()))
	}
	return st.rowFull("MERGED", green+bold, pr, "")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func fetchAll(ctx context.Context, c *adapter.Client, urls []string, jobs int) ([]adapter.PR, error) {
	prs := make([]adapter.PR, len(urls))
	errs := make([]error, len(urls))
	forEach(len(urls), jobs, func(i int) { prs[i], errs[i] = c.View(ctx, urls[i]) })
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return prs, nil
}

// forEach runs fn(0..n-1) with at most jobs in flight.
func forEach(n, jobs int, fn func(int)) {
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}()
	}
	wg.Wait()
}
