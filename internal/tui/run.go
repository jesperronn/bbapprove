package tui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"bbapprove/internal/adapter"
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
	m := New(Config{
		Backend: ClientBackend{Client: client}, IgnorePath: ignore.DefaultPath(),
		Check: rc.Check, Concurrency: jobs, Ctx: ctx,
	})
	p := tea.NewProgram(m, tea.WithInput(o.Stdin), tea.WithOutput(o.Stdout), tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(o.Stderr, err)
		return 1
	}
	return 0
}
