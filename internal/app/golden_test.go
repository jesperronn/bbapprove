package app

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bbapprove/internal/style"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/")

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file (run: go test ./internal/app -run Golden -update)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenPlainHelp(t *testing.T) {
	checkGolden(t, "help.golden", usage("v1.2.3"))
}

func printConfigRun(t *testing.T, env []string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte("repository = \"https://bitbucket.example.com\"\nauthor_email = \"bot@example.com\"\n"), 0o600)
	var so, se bytes.Buffer
	code := Run(context.Background(), Options{
		Args:    []string{"--print-config", "--config", cfg, "--disallow", "a,b"},
		Environ: append([]string{"XDG_CONFIG_HOME=" + dir, "BBAPPROVE_UPDATE_REPO=example/repo"}, env...),
		RepoDir: dir, Stdout: &so, Stderr: &se,
	})
	if code != 0 {
		t.Fatalf("code %d: %s", code, se.String())
	}
	return strings.ReplaceAll(so.String(), dir, "$DIR")
}

func TestGoldenPlainPrintConfig(t *testing.T) {
	checkGolden(t, "print_config.golden", printConfigRun(t, []string{"NO_COLOR=1"}))
}

func TestGoldenPlainDoctorHelp(t *testing.T) {
	checkGolden(t, "doctor_help.golden", doctorUsage())
}

func TestGoldenPlainHelpTopics(t *testing.T) {
	checkGolden(t, "help_topics.golden", topicListFor(style.Theme{}, "v1.2.3"))
	for _, h := range helpTopics {
		got, ok := topicUsageFor(style.Theme{}, h.name)
		if !ok {
			t.Fatalf("topic %s not found", h.name)
		}
		checkGolden(t, "help_"+h.name+".golden", got)
	}
}
