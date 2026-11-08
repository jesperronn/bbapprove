package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/term"

	tea "github.com/charmbracelet/bubbletea"

	"bbapprove/internal/adapter"
	"bbapprove/internal/app"
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
	disallow, token := env("BBAPPROVE_DISALLOW"), env("BBAPPROVE_TOKEN")
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
		case a == "--token" || strings.HasPrefix(a, "--token="):
			token = next()
		}
	}
	email := env("BBAPPROVE_AUTHOR_EMAIL")
	if email == "" {
		email = "renovate-bot@example.com"
	}
	file := env("BBAPPROVE_DISALLOW_FILE")
	if file == "" {
		file = filepath.Join(o.RepoDir, "disallow.txt")
	}
	path := env("BBAPPROVE_ADAPTER")
	if path == "" {
		path = filepath.Join(o.RepoDir, "adapters", "bitbucket")
	}
	jobs := runtime.NumCPU()
	if n, err := strconv.Atoi(env("BBAPPROVE_JOBS")); err == nil && n > 0 {
		jobs = n
	}
	rc := rules.Config{AuthorEmail: email, Disallow: rules.LoadDisallow(file, disallow)}
	client := &adapter.Client{Path: path, Token: token, Environ: o.Environ}
	if token == "" {
		t, err := client.AuthToken(ctx)
		if err != nil {
			fmt.Fprintln(o.Stderr, err)
			return 1
		}
		client.Token = t
	}
	mouse := MouseEnabled(env("BBAPPROVE_MOUSE"), o.Args)
	m := New(Config{
		Backend: ClientBackend{Client: client}, IgnorePath: ignore.DefaultPath(),
		Check: rc.Check, Concurrency: jobs, Ctx: ctx, Version: o.Version,
		NoColor: env("NO_COLOR") != "", NoMouse: !mouse, Summary: summaryStyle(o.Stdout, env),
	})
	return runModel(ctx, o, m, mouse)
}

// runModel runs the program for m and prints the exit summary. Split from Run so tests can inject a
// model that has a fake Backend.
func runModel(ctx context.Context, o RunOptions, m *Model, mouse bool) int {
	opts := []tea.ProgramOption{tea.WithInput(o.Stdin), tea.WithOutput(o.Stdout), tea.WithAltScreen(), tea.WithContext(ctx)}
	if mouse {
		opts = append(opts, tea.WithMouseAllMotion())
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
