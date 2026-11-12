package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"bbapprove/internal/adapter"
)

func TestOpenCommand(t *testing.T) {
	const url = "https://bb.example.com/projects/P/repos/r/pull-requests/1?a=1&b=2"
	for _, tc := range []struct {
		goos, name string
		args       []string
	}{
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", url}},
		{"darwin", "open", []string{url}},
		{"linux", "xdg-open", []string{url}},
		{"freebsd", "xdg-open", []string{url}},
	} {
		name, args := OpenCommand(tc.goos, url)
		if name != tc.name || !reflect.DeepEqual(args, tc.args) {
			t.Errorf("%s: OpenCommand = %q %q, want %q %q", tc.goos, name, args, tc.name, tc.args)
		}
	}
}

func TestApproveThenMerge(t *testing.T) {
	pr := adapter.PR{URL: "https://bb/pr/1"}
	approve := func(err error) func(context.Context, string) error {
		return func(context.Context, string) error { return err }
	}
	merge := func(warns []string, err error) func(context.Context, string) ([]string, error) {
		return func(context.Context, string) ([]string, error) { return warns, err }
	}
	ctx := context.Background()

	t.Run("approve fails, merge ok: warning, no error", func(t *testing.T) {
		w, err := approveThenMerge(ctx, pr, approve(errors.New("boom\nstderr")), merge([]string{"branch kept"}, nil))
		if err != nil || w != "approve failed: boom stderr; branch kept" {
			t.Fatalf("warning %q err %v", w, err)
		}
	})
	t.Run("approve ok, merge fails: PartialError", func(t *testing.T) {
		_, err := approveThenMerge(ctx, pr, approve(nil), merge(nil, errors.New("conflict")))
		var pe *PartialError
		if !errors.As(err, &pe) {
			t.Fatalf("want *PartialError, got %T %v", err, err)
		}
	})
	t.Run("both fail: plain error naming both", func(t *testing.T) {
		_, err := approveThenMerge(ctx, pr, approve(errors.New("no rights")), merge(nil, errors.New("conflict")))
		var pe *PartialError
		if err == nil || errors.As(err, &pe) || !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "approve failed: no rights") {
			t.Fatalf("got %T %v", err, err)
		}
	})
	t.Run("both ok: no warning", func(t *testing.T) {
		if w, err := approveThenMerge(ctx, pr, approve(nil), merge(nil, nil)); w != "" || err != nil {
			t.Fatalf("%q %v", w, err)
		}
	})
}
