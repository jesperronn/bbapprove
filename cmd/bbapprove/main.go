// Command bbapprove-go: Go port of the non-interactive bbapprove core.
package main

import (
	"context"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"bbapprove/internal/app"
)

func main() {
	fd := int(os.Stdout.Fd())
	tty := term.IsTerminal(fd)
	width := 0
	if tty {
		if w, _, err := term.GetSize(fd); err == nil {
			width = w
		}
	}
	os.Exit(app.Run(context.Background(), app.Options{
		Args: os.Args[1:], Environ: os.Environ(), RepoDir: repoDir(),
		Stdout: os.Stdout, Stderr: os.Stderr, IsTTY: tty, Width: width,
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
