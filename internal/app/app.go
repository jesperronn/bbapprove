// Package app implements the bbapprove command: dry run and --apply.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"bbapprove/internal/adapter"
	"bbapprove/internal/config"
	"bbapprove/internal/doctor"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
	"bbapprove/internal/style"
	"bbapprove/internal/update"
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
	// First-run setup (see config.Setup). Interactive reports stdin AND stderr are terminals; nil means
	// no (CI, pipes, tests). Stdin feeds the Y/n answer; Editor nil runs $VISUAL/$EDITOR for real.
	Interactive func() bool
	Stdin       io.Reader
	Editor      config.EditorRunner
	// --outdated / --update (see package update). UpdateRunner nil runs gh/git for real; GOOS/GOARCH
	// default to the running platform. RepoDir is the install root they inspect and never modify.
	UpdateRunner update.Runner
	GOOS, GOARCH string
	// doctor: nil seams use the real exec.LookPath / os.Executable.
	LookPath   func(string) (string, error)
	Executable func() (string, error)
}

func (o Options) firstRun() config.FirstRun {
	return config.FirstRun{IsTTY: o.Interactive, In: o.Stdin, Out: o.Stderr, Editor: o.Editor}
}

// ignorePath is the per-user ignore list: Options.IgnorePath, else $XDG_CONFIG_HOME/bbapprove/ignore.
func (o Options) ignorePath() string {
	if o.IgnorePath != "" {
		return o.IgnorePath
	}
	if x := o.env("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "bbapprove", "ignore")
	}
	return ignore.DefaultPath()
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

Usage: bbapprove [--apply] [--hide-ignored] [--disallow a,b] [--config FILE]
                 [--print-config] [PR_URL...]
       bbapprove --init [--force] [--config FILE]
       bbapprove doctor [--offline] [--config FILE]
       bbapprove --outdated [--pre]
       bbapprove --update [--yes] [--pre]

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
  - author email is the configured author_email (config file, BBAPPROVE_AUTHOR_EMAIL; no default)
  - title starts with "chore(deps):" or "fix(deps):"
  - title does not mention a disallowed dependency (config "disallow", --disallow)
  - update is minor/patch/digest (title "to vN" only, or branch renovate/major-*, is major)
  - not already approved by you
  - not on your ignore list (a PR on it is IGNORED whatever else applies, and never acted on)

Configuration: a TOML file (see bbapprove.config.template) with type, repository,
author_email, disallow, update_repo. Found via --config / BBAPPROVE_CONFIG, else <repo>/bbapprove.config.toml, else
$XDG_CONFIG_HOME/bbapprove/config.toml (default ~/.config/...). Precedence: command line > env > file >
default. repository and author_email have no default. With no config file at all, an interactive run
offers to create $XDG_CONFIG_HOME/bbapprove/config.toml from the template and open it in $VISUAL/$EDITOR;
--init does that on request (never overwrites an existing file unless --force; --config FILE or BBAPPROVE_CONFIG picks the
path). The template is built into the binary. --print-config shows the effective settings
(no secrets) and where each came from.
The API token comes only from the environment: export BBAPPROVE_TOKEN=... (see README, section Token).
Without it a run that needs the forge fails at once; --token / --token-name no longer exist.

doctor prints diagnostics and needs no token or forge access: version and update status, which config
file is in use (every candidate path and why), where each setting came from, the adapter, whether
BBAPPROVE_TOKEN is set (never its value), the ignore list (path and entries: what makes a row IGNORED), the disallow
lists, BBAPPROVE_* variables and tools (git, gh). Read-only; --offline skips the update check.
Exit 0 all good (warnings allowed), 1 a problem was found.

--outdated checks (read-only) whether a newer release exists: it asks "gh release list" for
jesperronn/bbapprove (override: update_repo in the config, BBAPPROVE_UPDATE_REPO) and, in a git
checkout without gh, "git ls-remote --tags origin". A pre-release build compares against the newest
release including pre-releases; a stable build against the newest stable one unless --pre. Exit 0 up to
date, 1 outdated, 2 could not check. bbapprove never checks by itself. --update does the same check,
prints a plan and asks "Proceed? [y/N]" (--yes skips the prompt; without a terminal it needs --yes).
A checkout is updated in place (git fetch --tags, git pull --ff-only, bin/build; needs a clean tree on
main). Any other install gets the new release unpacked next to it (bbapprove-<tag>-<os>-<arch>, SHA-256
verified, needs gh): the running tree is never modified, the commands to switch and to roll back are
printed. Neither flag needs a config file, token or forge access; both are exclusive with --apply and PR_URLs.

Env: BBAPPROVE_CONFIG, BBAPPROVE_UPDATE_REPO, BBAPPROVE_REPOSITORY, BBAPPROVE_AUTHOR_EMAIL,
     BBAPPROVE_DISALLOW, BBAPPROVE_DISALLOW_FILE, BBAPPROVE_JOBS, BBAPPROVE_ADAPTER, BBAPPROVE_TOKEN (required to talk to the forge),
     BBAPPROVE_MOUSE, BBAPPROVE_KITTY; NO_COLOR, FORCE_COLOR.
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
		disallow string
		cfgFile  string
		printCfg bool
		initCfg  bool
		force    bool
		outdated bool
		doUpdate bool
		pre      bool
		yes      bool
		wantTUI  bool
		offline  bool
		doDoctor bool
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
			wantTUI = true
		case a == "--disallow" || strings.HasPrefix(a, "--disallow="):
			v, ok := val()
			if !ok {
				fmt.Fprintln(o.Stderr, "--disallow needs a value")
				return 2
			}
			disallow = v
		case a == "--init":
			initCfg = true
		case a == "--force":
			force = true
		case a == "--outdated":
			outdated = true
		case a == "--update":
			doUpdate = true
		case a == "--pre":
			pre = true
		case a == "--offline":
			offline = true
		case a == "--yes":
			yes = true
		case a == "--print-config":
			printCfg = true
		case config.IsRemovedTokenFlag(a):
			fmt.Fprintln(o.Stderr, config.RemovedTokenFlagHelp)
			return 2
		case a == "--config" || strings.HasPrefix(a, "--config="):
			v, ok := val()
			if !ok {
				fmt.Fprintln(o.Stderr, "--config needs a value")
				return 2
			}
			cfgFile = v
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(o.Stderr, "Unknown option: %s\n%s", a, usage(o.Version))
			return 2
		case a == "doctor" && len(targets) == 0 && !doDoctor:
			doDoctor = true
		default:
			targets = append(targets, a)
		}
	}

	if doDoctor {
		if apply || hideIgn || initCfg || force || outdated || doUpdate || pre || yes || printCfg || len(targets) > 0 {
			fmt.Fprintln(o.Stderr, "doctor only takes --offline, --config and --disallow")
			return 2
		}
		return runDoctor(ctx, o, cfgFile, disallow, offline)
	}
	if offline {
		fmt.Fprintln(o.Stderr, "--offline only applies to doctor")
		return 2
	}
	for _, t := range targets {
		if !strings.Contains(t, "://") {
			fmt.Fprintf(o.Stderr, "Not a PR URL: %s\n(subcommands: doctor; see bbapprove --help)\n", t)
			return 2
		}
	}

	if outdated || doUpdate {
		return runUpdate(ctx, o, updateFlags{
			outdated: outdated, update: doUpdate, pre: pre, yes: yes, apply: apply, init: initCfg,
			printCfg: printCfg, targets: len(targets), cfgFile: cfgFile,
		})
	}
	if pre || yes {
		fmt.Fprintln(o.Stderr, "--pre and --yes only apply to --outdated / --update")
		return 2
	}
	if initCfg {
		return config.Init(ctx, o.env, cfgFile, force, o.firstRun(), o.Stdout)
	}
	if force {
		fmt.Fprintln(o.Stderr, "--force only applies to --init")
		return 2
	}
	if wantTUI && !printCfg {
		fmt.Fprintln(o.Stderr, "interactive mode: not implemented yet")
		return 2
	}
	cin := config.Input{
		Env: o.env, RepoDir: o.RepoDir, ConfigFlag: cfgFile, DisallowFlag: disallow,
	}
	cfg, err := config.Load(cin)
	if err != nil {
		fmt.Fprintf(o.Stderr, "bbapprove: %v\n", err)
		return 1
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintf(o.Stderr, "bbapprove: warning: %s\n", w)
	}
	if printCfg {
		io.WriteString(o.Stdout, cfg.Describe())
		return 0
	}
	if err := cfg.Missing(); err != nil {
		if cfg.File != "" {
			fmt.Fprintln(o.Stderr, err)
			return 1
		}
		var code int
		var cont bool
		if cfg, code, cont = config.Setup(ctx, cin, cfg, o.firstRun()); !cont {
			return code
		}
	}
	token := o.env("BBAPPROVE_TOKEN")
	if token == "" { // fail fast, before the adapter or the ignore list is touched
		fmt.Fprintln(o.Stderr, config.TokenMissingHelp)
		return 1
	}
	adapterPath := cfg.Adapter.Value
	jobs := runtime.NumCPU()
	if n, err := strconv.Atoi(o.env("BBAPPROVE_JOBS")); err == nil && n > 0 {
		jobs = n
	}

	ignorePath := o.ignorePath()
	// Fail closed: an unreadable ignore list must never be treated as empty, or an ignored PR could be merged.
	ignored, err := ignore.Load(ignorePath)
	if err != nil {
		fmt.Fprintf(o.Stderr, "cannot read ignore list: %v\n", err)
		return 1
	}

	rc := rules.Config{AuthorEmail: cfg.AuthorEmail.Value, Disallow: cfg.Disallow}
	client := &adapter.Client{Path: adapterPath, Token: token, Environ: cfg.ChildEnv(o.Environ)}

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
	hidden, shown := 0, 0
	for _, pr := range prs {
		kind, reason := rc.Classify(ignored, pr)
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
				shown++
				out.line(st.Row("IGNORED", dim, pr, reason))
			}
		default:
			out.line(st.Row("SKIP", yellow, pr, reason))
		}
	}
	if hidden > 0 {
		out.line(st.c(dim, fmt.Sprintf("%d ignored (hidden) via %s (see: bbapprove doctor)", hidden, ignorePath)))
	}
	if shown > 0 {
		out.line(st.c(dim, fmt.Sprintf("%d ignored via %s (see: bbapprove doctor)", shown, ignorePath)))
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
	return style.Enabled(o.env, o.IsTTY)
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

type updateFlags struct {
	outdated, update, pre, yes, apply, init, printCfg bool
	targets                                           int
	cfgFile                                           string
}

// runUpdate handles --outdated and --update. It needs no config file, token, adapter or forge access:
// a config that cannot be read only costs the update_repo setting (env and the default still work).
func runUpdate(ctx context.Context, o Options, f updateFlags) int {
	switch {
	case f.outdated && f.update:
		fmt.Fprintln(o.Stderr, "--outdated and --update cannot be combined (--update checks first anyway)")
		return 2
	case f.apply || f.targets > 0 || f.init || f.printCfg:
		fmt.Fprintln(o.Stderr, "--outdated / --update cannot be combined with --apply, --init, --print-config or PR URLs")
		return 2
	case f.yes && !f.update:
		fmt.Fprintln(o.Stderr, "--yes only applies to --update")
		return 2
	}
	slug := o.env("BBAPPROVE_UPDATE_REPO")
	if slug == "" {
		slug = config.DefaultUpdateRepo
		cfg, err := config.Load(config.Input{Env: o.env, RepoDir: o.RepoDir, ConfigFlag: f.cfgFile})
		if err != nil {
			fmt.Fprintf(o.Stderr, "bbapprove: warning: %v; using the default update repository\n", err)
		} else {
			slug = cfg.UpdateRepo.Value
		}
	}
	uo := update.Options{
		Runner: o.UpdateRunner, Stdin: o.Stdin, Stdout: o.Stdout, Stderr: o.Stderr,
		IsTTY: o.IsTTY && o.Interactive != nil && o.Interactive(),
		GOOS:  o.GOOS, GOARCH: o.GOARCH, InstallRoot: o.RepoDir, Version: o.Version, Slug: slug,
		PathEnv: o.env("PATH"), Pre: f.pre, Yes: f.yes,
	}
	if uo.GOOS == "" {
		uo.GOOS = runtime.GOOS
	}
	if uo.GOARCH == "" {
		uo.GOARCH = runtime.GOARCH
	}
	if f.update {
		return update.Update(ctx, uo)
	}
	return update.Outdated(ctx, uo)
}

// runDoctor wires the real process seams into package doctor.
func runDoctor(ctx context.Context, o Options, cfgFile, disallow string, offline bool) int {
	d := doctor.Options{
		Environ: o.Environ, Stdout: o.Stdout, Stderr: o.Stderr, IsTTY: o.IsTTY,
		Runner: o.UpdateRunner, LookPath: o.LookPath, Executable: o.Executable,
		Version: o.Version, GOOS: o.GOOS, GOARCH: o.GOARCH, GoVersion: runtime.Version(),
		InstallRoot: o.RepoDir, ConfigFlag: cfgFile, DisallowFlag: disallow,
		IgnorePath: o.ignorePath(), Offline: offline, Getwd: os.Getwd, HomeDir: os.UserHomeDir,
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.Executable == nil {
		d.Executable = os.Executable
	}
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}
	if d.GOARCH == "" {
		d.GOARCH = runtime.GOARCH
	}
	return doctor.Run(ctx, d)
}
