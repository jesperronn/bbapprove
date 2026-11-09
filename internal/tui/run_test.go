package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunFailsFastWithoutConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var se bytes.Buffer
	code := Run(context.Background(), RunOptions{Environ: []string{"BBAPPROVE_ADAPTER=/nonexistent"}, RepoDir: t.TempDir(), Stderr: &se})
	if code != 1 || !strings.Contains(se.String(), `"repository" is not set`) || !strings.Contains(se.String(), "bbapprove.config.template") {
		t.Fatalf("code %d err %q", code, se.String())
	}
}

// -i shares config.Setup with the plain CLI: same non-interactive message, same interactive flow.
func TestRunFirstRunSharesSetup(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfgPath := filepath.Join(xdg, "bbapprove", "config.toml")
	env := []string{"BBAPPROVE_ADAPTER=/nonexistent", "XDG_CONFIG_HOME=" + xdg}

	var se bytes.Buffer
	code := Run(context.Background(), RunOptions{Args: []string{"-i"}, Environ: env, RepoDir: t.TempDir(), Stderr: &se})
	if code != 1 || !strings.Contains(se.String(), "bbapprove --init") || !strings.Contains(se.String(), cfgPath) {
		t.Fatalf("non-TTY: code %d err %q", code, se.String())
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatal("file written without a TTY")
	}

	// TTY, user says yes, fake editor leaves the config incomplete: exits 1 naming the path.
	se.Reset()
	var gotPath string
	code = Run(context.Background(), RunOptions{
		Args: []string{"-i"}, Environ: append(env, "EDITOR=ed"), RepoDir: t.TempDir(), Stderr: &se,
		Stdin: strings.NewReader("y\n"), Interactive: func() bool { return true },
		Editor: func(_ context.Context, _ []string, p string) error {
			gotPath = p
			return os.WriteFile(p, []byte("type = \"bitbucket\"\n"), 0o600)
		},
	})
	if code != 1 || gotPath != cfgPath || !strings.Contains(se.String(), "does not set it") || !strings.Contains(se.String(), cfgPath) {
		t.Fatalf("TTY: code %d path %q err %q", code, gotPath, se.String())
	}
}
