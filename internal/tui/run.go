package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/term"

	tea "charm.land/bubbletea/v2"

	"bbapprove/internal/adapter"
	"bbapprove/internal/app"
	"bbapprove/internal/config"
	"bbapprove/internal/ignore"
	"bbapprove/internal/rules"
)

// RunOptions mirrors app.Options for the interactive mode.
type RunOptions struct {
	Args    []string
	Environ []string
	RepoDir string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Version string
	// First-run setup hooks, as in app.Options (nil Interactive means non-interactive).
	Interactive func() bool
	Editor      config.EditorRunner
	// Width and Height set the initial window size when positive. Only for headless runs (tests):
	// with a real terminal the size is detected, and without one Bubble Tea v2 draws nothing at 0x0.
	Width, Height int
}

// Run configures the TUI from args/env (same variables as the CLI) and runs it. Returns the exit code.
func Run(ctx context.Context, o RunOptions) int {
	env := func(key string) string {
		v := ""
		for _, kv := range o.Environ {
			if k, val, ok := strings.Cut(kv, "="); ok && k == key {
				v = val
			}
		}
		return v
	}
	var disallow, cfgFile string
	for i := 0; i < len(o.Args); i++ {
		a := o.Args[i]
		next := func() string {
			if _, v, ok := strings.Cut(a, "="); ok {
				return v
			}
			if i+1 < len(o.Args) {
				i++
				return o.Args[i]
			}
			return ""
		}
		switch {
		case a == "--disallow" || strings.HasPrefix(a, "--disallow="):
			disallow = next()
		case config.IsRemovedTokenFlag(a):
			fmt.Fprintln(o.Stderr, config.RemovedTokenFlagHelp)
			return 2
		case a == "--config" || strings.HasPrefix(a, "--config="):
			cfgFile = next()
		}
	}
	cin := config.Input{
		Env: env, RepoDir: o.RepoDir, ConfigFlag: cfgFile, DisallowFlag: disallow,
	}
	cfg, err := config.Load(cin)
	if err != nil {
		fmt.Fprintf(o.Stderr, "bbapprove: %v\n", err)
		return 1
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintf(o.Stderr, "bbapprove: warning: %s\n", w)
	}
	if err := cfg.Missing(); err != nil {
		if cfg.File != "" {
			fmt.Fprintln(o.Stderr, err)
			return 1
		}
		var code int
		var cont bool
		if cfg, code, cont = config.Setup(ctx, cin, cfg, config.FirstRun{IsTTY: o.Interactive, In: o.Stdin, Out: o.Stderr, Editor: o.Editor}); !cont {
			return code
		}
	}
	token := env("BBAPPROVE_TOKEN")
	if token == "" { // fail fast, before the adapter is touched
		fmt.Fprintln(o.Stderr, config.TokenMissingHelp)
		return 1
	}
	path := cfg.Adapter.Value
	jobs := runtime.NumCPU()
	if n, err := strconv.Atoi(env("BBAPPROVE_JOBS")); err == nil && n > 0 {
		jobs = n
	}
	rc := rules.Config{AuthorEmail: cfg.AuthorEmail.Value, Disallow: cfg.Disallow}
	client := &adapter.Client{Path: path, Token: token, Environ: cfg.ChildEnv(o.Environ)}
	mouse := MouseEnabled(env("BBAPPROVE_MOUSE"), o.Args)
	m := New(Config{
		Backend: ClientBackend{Client: client}, IgnorePath: ignore.DefaultPath(),
		Check: rc.Check, Concurrency: jobs, Ctx: ctx, Version: o.Version,
		NoColor: env("NO_COLOR") != "", NoMouse: !mouse, NoKitty: env("BBAPPROVE_KITTY") == "0", Summary: summaryStyle(o.Stdout, env),
	})
	return runModel(ctx, o, m, mouse)
}

// runModel runs the program for m and prints the exit summary. Split from Run so tests can inject a
// model that has a fake Backend.
func runModel(ctx context.Context, o RunOptions, m *Model, mouse bool) int {
	m.cfg.NoMouse = !mouse // mouse reporting is requested by Model.View
	opts := []tea.ProgramOption{tea.WithInput(o.Stdin), tea.WithOutput(o.Stdout), tea.WithContext(ctx)}
	if o.Width > 0 && o.Height > 0 {
		opts = append(opts, tea.WithWindowSize(o.Width, o.Height))
	}
	p := tea.NewProgram(m, opts...)
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(o.Stderr, err)
		return 1
	}
	// The alt screen is gone now: print the session summary so it stays in the scrollback.
	if fm, ok := final.(*Model); ok {
		fmt.Fprint(o.Stdout, fm.Summary())
	}
	return 0
}

// summaryStyle colours the exit summary like the CLI table (never with NO_COLOR; only on a TTY or
// with FORCE_COLOR) and truncates its lines to the terminal width only when stdout is a TTY.
func summaryStyle(w io.Writer, env func(string) string) app.Style {
	st := app.Style{}
	tty := false
	if f, ok := w.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		tty = true
		if cols, _, err := term.GetSize(int(f.Fd())); err == nil {
			st.Width = cols
		}
	}
	st.Color = env("NO_COLOR") == "" && (tty || env("FORCE_COLOR") != "")
	return st
}

// MouseEnabled reports whether mouse capture is on: it is unless BBAPPROVE_MOUSE=0 or --no-mouse.
func MouseEnabled(envValue string, args []string) bool {
	if envValue == "0" {
		return false
	}
	for _, a := range args {
		if a == "--no-mouse" {
			return false
		}
	}
	return true
}
