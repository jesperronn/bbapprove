// Package tui is the interactive Bubble Tea front end of bbapprove.
package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"bbapprove/internal/adapter"
)

// Backend holds every forge side effect of the TUI so the model can be tested with a fake.
type Backend interface {
	List(ctx context.Context) ([]adapter.PR, error)
	// Approve approves the PR without merging it.
	Approve(ctx context.Context, pr adapter.PR) error
	// ApproveAndMerge approves then merges; a failed approval does not stop the merge, it becomes a warning
	// ("approve failed: ..."). The error text is the adapter stderr; when the approval succeeded but the
	// merge failed the error is a *PartialError. On success warning is what the adapter reported on stderr
	// (one line, lines joined with "; "), "" when there was none.
	ApproveAndMerge(ctx context.Context, pr adapter.PR) (warning string, err error)
	// Comment posts a comment on the PR.
	Comment(ctx context.Context, pr adapter.PR, body string) error
	// Decline declines (closes) the PR, posting comment first when it is non-empty.
	Decline(ctx context.Context, pr adapter.PR, comment string) error
}

// PartialError reports an approval that went through while the merge did not.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// ClientBackend implements Backend on top of adapter.Client.
type ClientBackend struct{ Client *adapter.Client }

func (b ClientBackend) List(ctx context.Context) ([]adapter.PR, error) { return b.Client.List(ctx) }

func (b ClientBackend) Approve(ctx context.Context, pr adapter.PR) error {
	return b.Client.Approve(ctx, pr.URL)
}

func (b ClientBackend) ApproveAndMerge(ctx context.Context, pr adapter.PR) (string, error) {
	return approveThenMerge(ctx, pr, b.Client.Approve, b.Client.MergeWarn)
}

// approveThenMerge runs the merge whatever the approval did: Bitbucket refuses the merge itself when the
// approval was really needed, with a clearer message than the approval failure.
func approveThenMerge(ctx context.Context, pr adapter.PR, approve func(context.Context, string) error,
	merge func(context.Context, string) ([]string, error)) (string, error) {
	aerr := approve(ctx, pr.URL)
	warns, err := merge(ctx, pr.URL)
	if err != nil {
		if aerr != nil {
			return "", fmt.Errorf("%s (approve failed: %s)", strings.TrimSpace(err.Error()), oneLine(aerr.Error()))
		}
		return "", &PartialError{Err: err}
	}
	if aerr != nil {
		warns = append([]string{"approve failed: " + oneLine(aerr.Error())}, warns...)
	}
	return strings.Join(warns, "; "), nil
}

func (b ClientBackend) Comment(ctx context.Context, pr adapter.PR, body string) error {
	return b.Client.Comment(ctx, pr.URL, body)
}

func (b ClientBackend) Decline(ctx context.Context, pr adapter.PR, comment string) error {
	return b.Client.Close(ctx, pr.URL, comment)
}

// Opener opens a URL in the user's browser.
type Opener func(url string) error

// OpenCommand is the program and arguments that open url in the default browser on goos: rundll32 on
// Windows (not `cmd /c start`, which would treat the & in a URL as a command separator), `open` on macOS,
// `xdg-open` elsewhere.
func OpenCommand(goos, url string) (name string, args []string) {
	switch goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		return "open", []string{url}
	}
	return "xdg-open", []string{url}
}

// DefaultOpener opens the URL with OpenCommand for the running OS.
func DefaultOpener(url string) error {
	name, args := OpenCommand(runtime.GOOS, url)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
