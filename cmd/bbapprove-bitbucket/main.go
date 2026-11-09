// Command bbapprove-bitbucket runs the native Bitbucket adapter as a standalone executable with the bash
// adapter's CLI. It exists so the characterization tests (internal/bbfake) can run the Go adapter the way they
// run the bash one. It is not shipped: the release builds only ./cmd/bbapprove.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"

	"bbapprove/internal/forge/bitbucket"
)

func main() {
	env := bitbucket.EnvFromOS()
	env.StderrIsTTY = term.IsTerminal(int(os.Stderr.Fd()))
	// Test hook only: lets the slow-server cases finish quickly (a Go duration, e.g. 300ms).
	if v := os.Getenv("BBAPPROVE_BITBUCKET_TEST_MAX_TIME"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad BBAPPROVE_BITBUCKET_TEST_MAX_TIME:", err)
			os.Exit(2)
		}
		env.MaxTime = d
	}
	os.Exit(bitbucket.Run(context.Background(), os.Args[1:], env, os.Stdout, os.Stderr))
}
