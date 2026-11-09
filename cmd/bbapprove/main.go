// Command bbapprove-go: Go port of the non-interactive bbapprove core.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"bbapprove/internal/app"
	"bbapprove/internal/tui"
)

// version is injected at build time: -ldflags "-X main.version=<git describe>" (see bin/build).
var version = "dev"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-V" {
			fmt.Println("bbapprove " + version)
			return
		}
	}
	fd := int(os.Stdout.Fd())
	tty := term.IsTerminal(fd)
	width := 0
	if tty {
		if w, _, err := term.GetSize(fd); err == nil {
			width = w
		}
	}
	// Setup prompts need a human on both ends: answers come from stdin, prompts go to stderr.
	setupTTY := func() bool { return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd())) }
	if wantsTUI(os.Args[1:]) {
		os.Exit(tui.Run(context.Background(), tui.RunOptions{
			Args: os.Args[1:], Environ: os.Environ(), RepoDir: repoDir(),
			Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Version: version,
			Interactive: setupTTY,
		}))
	}
	os.Exit(app.Run(context.Background(), app.Options{
		Args: os.Args[1:], Environ: os.Environ(), RepoDir: repoDir(),
		Stdout: os.Stdout, Stderr: os.Stderr, IsTTY: tty, Width: width, Version: version,
		Interactive: setupTTY, Stdin: os.Stdin,
	}))
}

// repoDir is the parent of the directory holding the binary (<repo>/bin/bbapprove-go -> <repo>).
func repoDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(filepath.Dir(exe))
}

// wantsTUI reports whether -i selects the interactive TUI. --print-config and --init are handled by
// the plain CLI, even with -i; so are doctor, --outdated and --update.
func wantsTUI(args []string) bool {
	interactive, plain := false, false
	for _, a := range args {
		interactive = interactive || a == "-i" || a == "--interactive"
		plain = plain || a == "doctor" || a == "--print-config" || a == "--init" || a == "--outdated" || a == "--update"
	}
	return interactive && !plain
}
