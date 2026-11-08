// Package tui is the interactive Bubble Tea front end of bbapprove.
package tui

import (
	"context"
	"os/exec"
	"runtime"

	"bbapprove/internal/adapter"
)

// Backend holds every forge side effect of the TUI so the model can be tested with a fake.
type Backend interface {
	List(ctx context.Context) ([]adapter.PR, error)
	// ApproveAndMerge approves then merges. The error text is the adapter stderr; when the approval
	// succeeded but the merge failed the error is a *PartialError.
	ApproveAndMerge(ctx context.Context, pr adapter.PR) error
}

// PartialError reports an approval that went through while the merge did not.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// ClientBackend implements Backend on top of adapter.Client.
type ClientBackend struct{ Client *adapter.Client }

func (b ClientBackend) List(ctx context.Context) ([]adapter.PR, error) { return b.Client.List(ctx) }

func (b ClientBackend) ApproveAndMerge(ctx context.Context, pr adapter.PR) error {
	if err := b.Client.Approve(ctx, pr.URL); err != nil {
		return err
	}
	if err := b.Client.Merge(ctx, pr.URL); err != nil {
		return &PartialError{Err: err}
	}
	return nil
}

// Opener opens a URL in the user's browser.
type Opener func(url string) error

// DefaultOpener uses `open` on darwin and `xdg-open` elsewhere.
func DefaultOpener(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
