// Package bitbucket is the native Go Bitbucket Server adapter: the same commands as the bash adapter in
// adapters/bitbucket, over net/http, with no curl, jq or bash. Run mirrors its CLI protocol (exit 0 success,
// 1 failure, 2 usage or configuration error); the typed functions on Client are the library surface.
//
// Behaviour is proven against the bash adapter's golden files (internal/bbfake). Where it differs on purpose
// the differences are listed in docs/architecture.md.
package bitbucket

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Default timeouts per request and merge retry settings, identical to the bash adapter.
const (
	DefaultConnectTimeout = 2700 * time.Millisecond
	DefaultMaxTime        = 3500 * time.Millisecond
	defaultMergeRetries   = 4
	defaultRetrySleep     = 2
)

// Env is everything the adapter reads from its surroundings. The zero value is usable except for BaseURL.
type Env struct {
	BaseURL      string // BBAPPROVE_BASE_URL
	Token        string // BBAPPROVE_TOKEN
	TokenName    string // BBAPPROVE_TOKEN_NAME: 1Password item for `auth token`
	MergeRetries string // BBAPPROVE_MERGE_RETRIES, empty means 4
	RetrySleep   string // BBAPPROVE_RETRY_SLEEP in seconds, empty means 2

	DeleteSourceBranch string // BBAPPROVE_DELETE_SOURCE_BRANCH: true|1|yes or false|0|no, any case; empty means true

	NoColor     string // NO_COLOR
	ForceColor  string // FORCE_COLOR
	StderrIsTTY bool   // whether stderr is a terminal (the dim 1Password notice)

	Stdin io.Reader // for `pr comment --body-file -`; nil reads as empty

	ConnectTimeout time.Duration // 0 means DefaultConnectTimeout
	MaxTime        time.Duration // 0 means DefaultMaxTime

	// Seams for tests; the zero value uses the real thing.
	Sleep     func(time.Duration)
	LookPath  func(name string) (string, error)
	RunOp     func(ctx context.Context, path string, args []string) (string, error)
	Transport http.RoundTripper
}

// EnvFromOS reads the adapter's variables from the process environment. StderrIsTTY is left false: the caller
// knows how to ask its terminal.
func EnvFromOS() Env {
	return Env{
		BaseURL:      os.Getenv("BBAPPROVE_BASE_URL"),
		Token:        os.Getenv("BBAPPROVE_TOKEN"),
		TokenName:    os.Getenv("BBAPPROVE_TOKEN_NAME"),
		MergeRetries: os.Getenv("BBAPPROVE_MERGE_RETRIES"),
		RetrySleep:   os.Getenv("BBAPPROVE_RETRY_SLEEP"),

		DeleteSourceBranch: os.Getenv("BBAPPROVE_DELETE_SOURCE_BRANCH"),
		NoColor:            os.Getenv("NO_COLOR"),
		ForceColor:         os.Getenv("FORCE_COLOR"),
		Stdin:              os.Stdin,
	}
}

// Error is a failure with the exit code the CLI reports for it: 1 for a failed operation, 2 for bad usage or
// configuration. Msg is the one line printed on stderr.
type Error struct {
	Msg  string
	Exit int
}

func (e *Error) Error() string { return e.Msg }

func failf(format string, a ...any) error  { return &Error{Msg: fmt.Sprintf(format, a...), Exit: 1} }
func usagef(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...), Exit: 2} }

// ExitCode is the exit code for err: 0 for nil, the Error's own, else 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Exit
	}
	return 1
}

func (e Env) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if e.Sleep != nil {
		e.Sleep(d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func (e Env) lookPath(name string) (string, error) {
	if e.LookPath != nil {
		return e.LookPath(name)
	}
	return exec.LookPath(name)
}

func (e Env) runOp(ctx context.Context, path string, args []string) (string, error) {
	if e.RunOp != nil {
		return e.RunOp(ctx, path, args)
	}
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- path comes from LookPath("op")
	var out strings.Builder
	cmd.Stdout = &out // stderr is discarded, like 2>/dev/null
	err := cmd.Run()
	return out.String(), err
}

// retryPolicy validates BBAPPROVE_MERGE_RETRIES (attempts, so 0 still tries once) and BBAPPROVE_RETRY_SLEEP.
func (e Env) retryPolicy() (attempts int, sleep time.Duration, err error) {
	attempts, err = nonNegative("BBAPPROVE_MERGE_RETRIES", e.MergeRetries, defaultMergeRetries)
	if err != nil {
		return 0, 0, err
	}
	secs, err := nonNegative("BBAPPROVE_RETRY_SLEEP", e.RetrySleep, defaultRetrySleep)
	if err != nil {
		return 0, 0, err
	}
	return attempts, time.Duration(secs) * time.Second, nil
}

func nonNegative(name, val string, def int) (int, error) {
	if val == "" {
		return def, nil
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 0 {
		return 0, usagef("bitbucket adapter: %s must be a non-negative integer, got %q", name, val)
	}
	return n, nil
}

// deleteSourceBranch parses BBAPPROVE_DELETE_SOURCE_BRANCH; empty (unset) means true, like the bash adapter.
func (e Env) deleteSourceBranch() (bool, error) {
	switch strings.ToLower(e.DeleteSourceBranch) {
	case "", "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	}
	return false, usagef("pr merge: BBAPPROVE_DELETE_SOURCE_BRANCH must be true or false, got: %s", e.DeleteSourceBranch)
}
