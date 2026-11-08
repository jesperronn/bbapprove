package tui

import (
	"bytes"
	"context"
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
